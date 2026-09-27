package engine

import (
	_ "embed"
	"fmt"
	"slices"
	"strconv"
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
	// CUDASASS lists the compute capabilities the CUDA payload carries SASS
	// for, as major*10+minor, from the cuda-sass directive. Empty means the
	// pin does not narrow them, and the engine's minCUDACompute floor alone
	// applies. See CoversCUDA.
	CUDASASS []int
	// CUDA12SASS is the same list for the legacy CUDA 12 payload
	// (`#! dso-cuda12`, `#! cuda12-sass`): the older cards the CUDA 13 payload
	// has no code for (Volta 7.0 on the dev snapshots). One process loads one
	// payload, so it is staged beside a second copy of the engine in
	// engines/cuda_v12 and chosen per load (CUDA12Dirs, Launch).
	CUDA12SASS []int
	Assets     []Asset
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

// CoversCUDA reports whether the pinned CUDA payload carries code for a device
// of this compute capability. SASS for major.minor runs on the same major at
// that minor or later, and on nothing else: sm_86 serves 8.6-8.9, sm_120f
// serves 12.x, and neither serves 8.0 or 9.0. A pin without cuda-sass does not
// narrow anything.
//
// The failure this prevents is silent: a device the DSO has no code for makes
// the engine run the load on the CPU, which reads as slowness, not as an error.
func (p Pin) CoversCUDA(major, minor int) bool {
	if len(p.CUDASASS) == 0 {
		return true
	}
	for _, cc := range p.CUDASASS {
		if major == cc/10 && minor >= cc%10 {
			return true
		}
	}
	return false
}

// CoversCUDA12 reports whether the pin's CUDA 12 payload carries code for a
// device of this compute capability, with CoversCUDA's matching rule. Unlike
// CoversCUDA, a pin that states no cuda12-sass covers nothing: the CUDA 12
// payload is optional and only ever serves what it names.
func (p Pin) CoversCUDA12(major, minor int) bool {
	if _, ok := p.CUDA12DSO("x86_64"); !ok {
		return false
	}
	for _, cc := range p.CUDA12SASS {
		if major == cc/10 && minor >= cc%10 {
			return true
		}
	}
	return false
}

// CUDA12DSO returns the CUDA 12 payload for an arch label, if the pin has one.
func (p Pin) CUDA12DSO(arch string) (Asset, bool) {
	for _, a := range p.Assets {
		if a.Kind == kindCUDA12DSO && a.Arch == arch {
			return a, true
		}
	}
	return Asset{}, false
}

// kindCUDA12DSO is the asset kind of a `#! dso-cuda12` row.
const kindCUDA12DSO = "dso-cuda12"

// HasFeature reports whether the pinned artifact declares a capability.
func (p Pin) HasFeature(name string) bool {
	return slices.Contains(p.Features, name)
}

// machineKeys are the directives a "#!" line may carry (see ParsePin).
var machineKeys = []string{"cuda-sass", "cuda12-sass", kindCUDA12DSO}

// DefaultPin is the pin compiled into this binary.
func DefaultPin() (Pin, error) { return ParsePin(pinText) }

// ParsePin reads the pin format described at the top of pin.txt.
func ParsePin(text string) (Pin, error) {
	var p Pin
	for n, raw := range strings.Split(text, "\n") {
		line := raw
		// "#! <key> <values>" is a machine-readable line inside a comment:
		// opencoti's own pin parsers skip every '#' line and refuse any bare
		// directive but repo/rev/tag and asset rows, so facts they add for
		// us ride there (mail #449). Only keys this parser knows are read;
		// an unknown one is a comment, so a new key cannot break a build.
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "#!"); ok {
			if f := strings.Fields(rest); len(f) > 0 && slices.Contains(machineKeys, f[0]) {
				line = rest
			}
		}
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
		case "cuda-sass", "cuda12-sass":
			if len(fields) < 2 {
				return Pin{}, fmt.Errorf("pin.txt:%d: %s needs at least one compute capability", n+1, fields[0])
			}
			for _, f := range fields[1:] {
				cc, err := strconv.Atoi(f)
				if err != nil || cc < 10 {
					return Pin{}, fmt.Errorf("pin.txt:%d: %s %q is not a compute capability written as major*10+minor (86, 120)", n+1, fields[0], f)
				}
				if fields[0] == "cuda12-sass" {
					p.CUDA12SASS = append(p.CUDA12SASS, cc)
				} else {
					p.CUDASASS = append(p.CUDASASS, cc)
				}
			}
		case "bin", "dso", kindCUDA12DSO:
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
	p.deriveAccelsFromDSOs()
	return p, nil
}

// deriveAccelsFromDSOs adds the (arch, backend) pairs the pin's OWN dso rows
// prove, on top of any stated explicitly.
//
// The payloads shipped are the ground truth about what the artifact can
// accelerate, and an `accel` row merely restates them. opencoti's dev
// publisher says so directly -- "key off the dso rows actually present" -- and
// snapshot 2609242056001 carries no accel rows at all, stating its payload set
// in a header comment instead. Keyed on accel rows alone that pin accelerates
// NOTHING, so every GPU load would route to llama.cpp while the pinned engine
// sat there holding a working Vulkan payload.
//
// Explicit rows are still honoured and still validated above: this only ever
// ADDS, so a pin that states its accels keeps behaving exactly as before.
func (p *Pin) deriveAccelsFromDSOs() {
	for _, a := range p.Assets {
		if a.Kind != "dso" {
			continue
		}
		arch, backend := splitDSOLabel(a.Arch)
		// A payload for a platform this pin ships no engine for accelerates
		// nothing here. Skipping rather than erroring keeps a cross-platform
		// snapshot usable: a pin may carry a win-x86_64 CUDA dso and no
		// win-x86_64 bin row, which is a Windows build's business, not ours.
		if _, ok := p.Asset(arch); !ok {
			continue
		}
		if !slices.Contains(p.Accels, Accel{Arch: arch, Backend: backend}) {
			p.Accels = append(p.Accels, Accel{Arch: arch, Backend: backend})
		}
	}
}

// splitDSOLabel reads a dso row's label as <arch>[-<backend>].
//
// The bare form is CUDA: it is the label opencoti has always used for the CUDA
// payload (`dso x86_64`), and renaming it would break every consumer of an
// existing snapshot, so the default has to stay what it already means.
func splitDSOLabel(label string) (arch string, backend Backend) {
	for suffix, b := range map[string]Backend{
		"-vulkan": BackendVulkan,
		"-rocm":   BackendROCm,
	} {
		if rest, ok := strings.CutSuffix(label, suffix); ok {
			return rest, b
		}
	}
	return label, BackendCUDA
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

// ArchFor is the arch label this pin packages for a build host: PackageArch,
// except that Windows falls back to the bare win-x86_64 bin when the pin
// carries no -gpu one. A dev snapshot publishes Windows that way -- the bare
// APE plus its CUDA payload as a win-x86_64 dso row -- and the dso is what
// deriveAccelsFromDSOs turns into Windows acceleration. The -gpu row still
// wins whenever both are present. cmake/opencoti-engine.cmake and the release
// workflow make the same choice.
func (p Pin) ArchFor(goos, goarch string) (string, error) {
	arch, err := PackageArch(goos, goarch)
	if err != nil {
		return "", err
	}
	if arch == "win-x86_64-gpu" {
		if _, ok := p.Asset(arch); !ok {
			if _, ok := p.Asset("win-x86_64"); ok {
				return "win-x86_64", nil
			}
		}
	}
	return arch, nil
}
