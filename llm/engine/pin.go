package engine

import (
	_ "embed"
	"fmt"
	"slices"
	"strings"
)

// pinText is the committed artifact pin. It is embedded rather than read from
// disk because its consumers are a running server and a test, neither of which
// can assume the source tree is present.
//
// cmake/opencoti-engine.cmake parses the same file with the same rules at
// build time. The format is deliberately trivial so the two parsers cannot
// drift; TestPinFormatIsWhatCMakeParses pins the assumptions CMake relies on.
//
//go:embed pin.txt
var pinText string

// Asset is one published engine artifact.
type Asset struct {
	Kind   string // "bin" (the engine) or "dso" (a side-loaded GPU payload)
	Arch   string // x86_64 | aarch64 | win-x86_64 | win-x86_64-gpu | universal
	Path   string // path within the Hugging Face repo
	SHA256 string
}

// Pin is the parsed pin file: where the artifacts live, which bytes are the
// right ones, and what that build can be asked to do.
type Pin struct {
	Repo string
	Rev  string
	Tag  string
	// Channel is release or dev. It is declared rather than guessed from the
	// repo name because the two channels may share a repo -- a dev channel
	// points at the release repo whenever nothing new is in flight -- and
	// because a build has to be able to say which one it is.
	Channel string
	// Features are the engine capabilities this artifact carries, named
	// explicitly. See the note on feature directives in pin.txt.
	Features []string
	// Accels are the backends this artifact can actually accelerate, per arch.
	// A release bin embeds its payloads; a dev snapshot is a bare APE that
	// accelerates only what its dso rows provide. Declaring it is what stops
	// routing handing a GPU load to an engine that would quietly serve it on
	// the CPU.
	Accels []Accel
	Assets []Asset
}

// Accel is one (arch, backend) pair the pinned artifact accelerates.
type Accel struct {
	Arch    string
	Backend Backend
}

// Channel values.
const (
	ChannelRelease = "release"
	ChannelDev     = "dev"
)

// Accelerates reports whether the pinned artifact carries a payload that runs
// this backend on this arch. CPU is always true: the host binary is the engine.
func (p Pin) Accelerates(arch string, b Backend) bool {
	if b == BackendCPU {
		return true
	}
	return slices.Contains(p.Accels, Accel{Arch: arch, Backend: b})
}

// HasFeature reports whether the pinned artifact declares a capability.
func (p Pin) HasFeature(name string) bool {
	return slices.Contains(p.Features, name)
}

// DefaultPin is the pin compiled into this binary.
func DefaultPin() (Pin, error) { return ParsePin(pinText) }

// ParsePin reads the pin format described at the top of pin.txt.
func ParsePin(text string) (Pin, error) {
	var p Pin
	for n, raw := range strings.Split(text, "\n") {
		line := raw
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "repo", "rev", "tag", "channel", "feature":
			if len(fields) != 2 {
				return Pin{}, fmt.Errorf("pin.txt:%d: %s takes exactly one value, got %d", n+1, fields[0], len(fields)-1)
			}
			switch fields[0] {
			case "repo":
				p.Repo = fields[1]
			case "rev":
				p.Rev = fields[1]
			case "tag":
				p.Tag = fields[1]
			case "channel":
				p.Channel = fields[1]
			case "feature":
				p.Features = append(p.Features, fields[1])
			}
		case "accel":
			if len(fields) != 3 {
				return Pin{}, fmt.Errorf("pin.txt:%d: accel row needs <arch> <backend>, got %d fields", n+1, len(fields)-1)
			}
			b := Backend(fields[2])
			if !slices.Contains(knownBackends, b) {
				return Pin{}, fmt.Errorf("pin.txt:%d: accel backend %q is not one of %v", n+1, fields[2], knownBackends)
			}
			p.Accels = append(p.Accels, Accel{Arch: fields[1], Backend: b})
		case "bin", "dso":
			if len(fields) != 4 {
				return Pin{}, fmt.Errorf("pin.txt:%d: asset row needs <kind> <arch> <path> <sha256>, got %d fields", n+1, len(fields))
			}
			p.Assets = append(p.Assets, Asset{Kind: fields[0], Arch: fields[1], Path: fields[2], SHA256: fields[3]})
		default:
			return Pin{}, fmt.Errorf("pin.txt:%d: unknown directive %q", n+1, fields[0])
		}
	}
	for _, missing := range []struct {
		name  string
		value string
	}{{"repo", p.Repo}, {"rev", p.Rev}, {"tag", p.Tag}, {"channel", p.Channel}} {
		if missing.value == "" {
			return Pin{}, fmt.Errorf("pin.txt: no %s directive", missing.name)
		}
	}
	// A channel is required rather than defaulted, so a dev pin can never be
	// mistaken for a release one by omission.
	if p.Channel != ChannelRelease && p.Channel != ChannelDev {
		return Pin{}, fmt.Errorf("pin.txt: channel %q is not %s or %s", p.Channel, ChannelRelease, ChannelDev)
	}
	// The revision must be an immutable commit, never a branch. opencoti
	// re-cuts a release in place: the c7 r2 re-cut replaced all five host
	// binaries under their existing names on `main`, which turned every
	// downstream pin that said `rev main` into a build that fetches bytes its
	// own sha256 rows reject. Naming the commit is what makes a pin a pin.
	if !isCommitRev(p.Rev) {
		return Pin{}, fmt.Errorf("pin.txt: rev %q is not a 40-character commit sha; a branch or tag can be moved under the pinned sha256 rows", p.Rev)
	}
	if len(p.Assets) == 0 {
		return Pin{}, fmt.Errorf("pin.txt: no asset rows")
	}
	// Claiming acceleration for an arch whose engine is not shipped is the one
	// inconsistency the format can catch on its own.
	for _, a := range p.Accels {
		if _, ok := p.Asset(a.Arch); !ok {
			return Pin{}, fmt.Errorf("pin.txt: accel %s %s has no bin row for %s", a.Arch, a.Backend, a.Arch)
		}
	}
	return p, nil
}

func isCommitRev(rev string) bool {
	if len(rev) != 40 {
		return false
	}
	return strings.TrimLeft(rev, "0123456789abcdef") == ""
}

// Asset returns the bin row for an arch label. dso rows are addressed with
// DSO: an arch can have both, and the engine is always the bin.
func (p Pin) Asset(arch string) (Asset, bool) {
	for _, a := range p.Assets {
		if a.Kind == "bin" && a.Arch == arch {
			return a, true
		}
	}
	return Asset{}, false
}

// DSO returns the side-loadable payload for an arch label, if the pin carries
// one. Release bins embed their payloads and self-extract, so this is normally
// empty; a dev snapshot ships the GPU payload beside the binary instead.
func (p Pin) DSO(arch string) (Asset, bool) {
	for _, a := range p.Assets {
		if a.Kind == "dso" && a.Arch == arch {
			return a, true
		}
	}
	return Asset{}, false
}

// URL is where the artifact is fetched from at build time. Hugging Face is the
// only source: the artifacts are larger than GitHub's release-asset cap.
func (p Pin) URL(a Asset) string {
	return "https://huggingface.co/" + p.Repo + "/resolve/" + p.Rev + "/" + a.Path
}

// PackageArch maps a build host onto the arch label packaged for it.
//
// Windows takes the -gpu variant deliberately. The bare win-x86_64 artifact is
// a tenth of the size but carries no GPU payload, and an installer that needs
// a second download to use the GPU is not an installer. Linux needs no such
// choice: those artifacts embed their payloads already.
//
// darwin is absent on purpose, not by omission: macOS keeps ollama's MLX path
// and never routes to this engine, so nothing is packaged for it.
func PackageArch(goos, goarch string) (string, error) {
	switch {
	case goos == "linux" && goarch == "amd64":
		return "x86_64", nil
	case goos == "linux" && goarch == "arm64":
		return "aarch64", nil
	case goos == "windows" && goarch == "amd64":
		return "win-x86_64-gpu", nil
	}
	return "", fmt.Errorf("no opencoti-llamafile artifact is packaged for %s/%s", goos, goarch)
}
