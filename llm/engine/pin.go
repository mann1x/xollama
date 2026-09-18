package engine

import (
	_ "embed"
	"fmt"
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
	Kind   string // "bin"
	Arch   string // x86_64 | aarch64 | win-x86_64 | win-x86_64-gpu | universal
	Path   string // path within the Hugging Face repo
	SHA256 string
}

// Pin is the parsed pin file: where the artifacts live and which bytes are
// the right ones.
type Pin struct {
	Repo   string
	Rev    string
	Tag    string
	Assets []Asset
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
		case "repo", "rev", "tag":
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
			}
		case "bin":
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
	}{{"repo", p.Repo}, {"rev", p.Rev}, {"tag", p.Tag}} {
		if missing.value == "" {
			return Pin{}, fmt.Errorf("pin.txt: no %s directive", missing.name)
		}
	}
	if len(p.Assets) == 0 {
		return Pin{}, fmt.Errorf("pin.txt: no asset rows")
	}
	return p, nil
}

// Asset returns the row for an arch label.
func (p Pin) Asset(arch string) (Asset, bool) {
	for _, a := range p.Assets {
		if a.Arch == arch {
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
