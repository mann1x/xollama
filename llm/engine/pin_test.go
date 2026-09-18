package engine

import (
	"regexp"
	"strings"
	"testing"
)

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestCommittedPinParses(t *testing.T) {
	p, err := DefaultPin()
	if err != nil {
		t.Fatalf("DefaultPin() = %v", err)
	}
	if p.Repo != "ManniX-ITA/opencoti-llamafile" {
		t.Errorf("Repo = %q", p.Repo)
	}
	if p.Rev == "" || p.Tag == "" {
		t.Errorf("Rev = %q, Tag = %q; both are provenance and must be set", p.Rev, p.Tag)
	}

	seen := map[string]bool{}
	for _, a := range p.Assets {
		if a.Kind != "bin" {
			t.Errorf("%s: kind = %q, want bin (dso rows are deliberately not carried)", a.Arch, a.Kind)
		}
		if !sha256Re.MatchString(a.SHA256) {
			t.Errorf("%s: sha256 = %q, want 64 lowercase hex", a.Arch, a.SHA256)
		}
		if seen[a.Arch] {
			t.Errorf("%s: duplicate arch row; Asset() would return whichever came first", a.Arch)
		}
		seen[a.Arch] = true
		if strings.HasPrefix(a.Path, "/") || strings.Contains(a.Path, "..") {
			t.Errorf("%s: path %q must be a plain repo-relative path", a.Arch, a.Path)
		}
	}
}

// TestEveryTestedPlatformHasAnArtifact is the invariant that keeps policy.go
// and pin.txt from drifting apart. Adding a platform to the tested matrix
// without publishing an artifact for it means the router sends loads to an
// engine the package does not ship, and the only symptom is a fallback log
// line on a user's machine.
func TestEveryTestedPlatformHasAnArtifact(t *testing.T) {
	p, err := DefaultPin()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range tested {
		goarch := map[string]string{"amd64": "amd64", "arm64": "arm64"}[s.Arch]
		arch, err := PackageArch(s.OS, goarch)
		if err != nil {
			t.Errorf("%s/%s is in the tested matrix but PackageArch says: %v", s.OS, s.Arch, err)
			continue
		}
		if _, ok := p.Asset(arch); !ok {
			t.Errorf("%s/%s maps to arch %q, which has no row in pin.txt", s.OS, s.Arch, arch)
		}
	}
}

func TestPackageArch(t *testing.T) {
	cases := []struct {
		goos, goarch, want string
		wantErr            bool
	}{
		{goos: "linux", goarch: "amd64", want: "x86_64"},
		{goos: "linux", goarch: "arm64", want: "aarch64"},
		// The GPU variant, not the bare one: an installer that needs a second
		// download before it can use the GPU is not an installer.
		{goos: "windows", goarch: "amd64", want: "win-x86_64-gpu"},
		// macOS keeps ollama's MLX path and never routes here.
		{goos: "darwin", goarch: "arm64", wantErr: true},
		{goos: "windows", goarch: "arm64", wantErr: true},
	}
	for _, tt := range cases {
		got, err := PackageArch(tt.goos, tt.goarch)
		if tt.wantErr {
			if err == nil {
				t.Errorf("PackageArch(%s, %s) = %q, want an error", tt.goos, tt.goarch, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("PackageArch(%s, %s) = %q, %v; want %q", tt.goos, tt.goarch, got, err, tt.want)
		}
	}
}

func TestPinURL(t *testing.T) {
	p := Pin{Repo: "o/r", Rev: "main"}
	got := p.URL(Asset{Path: "a.llamafile"})
	want := "https://huggingface.co/o/r/resolve/main/a.llamafile"
	if got != want {
		t.Errorf("URL() = %q, want %q", got, want)
	}
}

// TestPinFormatIsWhatCMakeParses pins the assumptions the CMake parser in
// cmake/opencoti-engine.cmake relies on. CMake cannot import this parser, so
// the two agree only as long as the format stays this simple.
func TestPinFormatIsWhatCMakeParses(t *testing.T) {
	p, err := ParsePin(`
# a comment line
repo  o/r      # trailing comments are stripped
rev   main
tag   v1

bin  x86_64  a.llamafile  ` + strings.Repeat("a", 64) + `
`)
	if err != nil {
		t.Fatalf("ParsePin() = %v", err)
	}
	if p.Repo != "o/r" || p.Rev != "main" || p.Tag != "v1" || len(p.Assets) != 1 {
		t.Fatalf("parsed = %+v", p)
	}
	if p.Assets[0].Path != "a.llamafile" {
		t.Errorf("Path = %q", p.Assets[0].Path)
	}
}

func TestParsePinRejectsMalformedInput(t *testing.T) {
	cases := map[string]string{
		"no repo":           "rev main\ntag v1\nbin x86_64 a " + strings.Repeat("a", 64),
		"no assets":         "repo o/r\nrev main\ntag v1",
		"short asset row":   "repo o/r\nrev main\ntag v1\nbin x86_64 a.llamafile",
		"unknown directive": "repo o/r\nrev main\ntag v1\nbanana x",
		"repo with 2 args":  "repo o/r extra\nrev main\ntag v1",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePin(text); err == nil {
				t.Error("ParsePin() = nil error, want a rejection")
			}
		})
	}
}

// TestPinIsASCII guards the CMake parser, which is byte-oriented. CMake's
// regex "." does not match the bytes of a multi-byte UTF-8 character, so a
// single em-dash in a comment once left the tail of that comment being parsed
// as a directive. cmake/opencoti-fetch.cmake no longer depends on this for
// whole-line comments, but keeping the file ASCII removes the class.
func TestPinIsASCII(t *testing.T) {
	for i, r := range pinText {
		if r > 127 {
			t.Errorf("pin.txt byte %d is %q (U+%04X); keep the file ASCII for the CMake parser", i, r, r)
		}
	}
}
