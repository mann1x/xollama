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
	// Deliberately NOT asserting which repo. xollama tracks opencoti's
	// development line, so the dev branch points at a different HF repo
	// whenever a cut is in flight; a test that named one would fail on exactly
	// the branch the arrangement exists for. The shape is what matters.
	if strings.Count(p.Repo, "/") != 1 || strings.HasPrefix(p.Repo, "/") || strings.HasSuffix(p.Repo, "/") {
		t.Errorf("Repo = %q, want an <owner>/<name> path", p.Repo)
	}
	if !isCommitRev(p.Rev) || p.Tag == "" || !isBuildID(p.Version) || !strings.HasSuffix(p.Tag, p.Version) {
		t.Errorf("Rev = %q, Tag = %q, Version = %q; all three are provenance and must be set", p.Rev, p.Tag, p.Version)
	}
	if p.Channel != ChannelRelease && p.Channel != ChannelDev {
		t.Errorf("Channel = %q, want %q or %q", p.Channel, ChannelRelease, ChannelDev)
	}

	// Two files of one platform under one staged name would overwrite each
	// other beside the engine.
	for _, arch := range platforms {
		if _, ok := p.Asset(arch); !ok {
			t.Errorf("%s: no engine", arch)
		}
		seen := map[string]bool{}
		for _, a := range p.Files(arch) {
			if !sha256Re.MatchString(a.SHA256) || a.Bytes <= 0 || !isCommitRev(a.Rev) || a.Repo == "" {
				t.Errorf("%s %s: sha256 %q, %d bytes, %s@%s", arch, a.StagedName(), a.SHA256, a.Bytes, a.Repo, a.Rev)
			}
			if seen[a.StagedName()] {
				t.Errorf("%s: two files are staged as %s", arch, a.StagedName())
			}
			seen[a.StagedName()] = true
		}
	}
}

// The index is ours and the component pins are opencoti's, vendored
// byte-identical: LoadPin proves each against the index, and this proves the
// index names every pin file in the directory, so none sits there unread.
func TestEveryVendoredPinIsNamedByTheIndex(t *testing.T) {
	index, err := pinFS.ReadFile(pinDir + "/index.txt")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := pinFS.ReadDir(pinDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "index.txt" {
			continue
		}
		if !strings.Contains(string(index), " pin/"+e.Name()+" ") {
			t.Errorf("%s/%s is not named by the index; remove it or take it", pinDir, e.Name())
		}
	}
}

// A package is staged from the pin's rows for its arch and from nothing else,
// so a library without its licence row ships without its licence. oc-espeak
// is eSpeak-ng (GPL 3.0 or later); oc-codec is ffmpeg and lame (LGPL), whose
// source and build references are in the media component's BUILD_INFO.md.
func TestEveryLicensedSidecarShipsItsText(t *testing.T) {
	p, err := DefaultPin()
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range platforms {
		have, licensed, info := map[string]bool{}, map[string]bool{}, false
		for _, s := range p.Sidecars(arch) {
			have[s.Role] = true
			if s.Role == SidecarLicence {
				licensed[s.For] = true
			}
			info = info || (s.Role == "build-info" && s.Component == "media")
		}
		for _, lib := range []string{"espeak", SidecarCodec} {
			if have[lib] && (!licensed[lib] || !info) {
				t.Errorf("%s: the pin stages the %s sidecar without its licence text (%v) or the media BUILD_INFO (%v)",
					arch, lib, licensed[lib], info)
			}
		}
	}
}

// Windows CUDA and Vulkan are rows of their own: with the row the backend is
// the engine's, without it it is refused to llama.cpp for a stated reason,
// never served by the engine on the CPU.
func TestTheCommittedPinRoutesWindowsByItsLibraryRows(t *testing.T) {
	p, err := DefaultPin()
	if err != nil {
		t.Fatal(err)
	}
	win := Platform{OS: "windows", Arch: "amd64"}
	for label, b := range map[string]Backend{"win-x86_64": BackendCUDA, "win-x86_64-vulkan": BackendVulkan} {
		_, has := p.DSO(label)
		if reason := pinUncoveredIn(p, win, b); has != (reason == "") {
			t.Errorf("windows %s: library row present = %v, refusal = %q", b, has, reason)
		}
	}
	// The sass line is what keeps a card the library has no code for off
	// these bytes: stated, and never covering Pascal.
	if len(p.CUDASASS) == 0 || p.CoversCUDA(6, 1) || !p.CoversCUDA(8, 6) {
		t.Errorf("the committed pin's CUDA sass is not in force: %v", p.CUDASASS)
	}
	if !p.Accelerates("x86_64", BackendCUDA) {
		t.Errorf("committed pin %s does not accelerate CUDA on x86_64", p.Tag)
	}
}

// TestEveryTestedPlatformIsServedOrRefused is the invariant that keeps
// policy.go and the pin from drifting apart. The original form of this test
// required an artifact for every tested platform, which was right while the
// fork only ever pinned a full release. A development snapshot ships a subset
// -- opencoti publishes no Windows or aarch64 GPU payload on that channel --
// so the invariant is now the one that actually matters: a tested platform
// must either be served by the pinned bytes or be refused for a stated reason.
// What must never happen is routing a load to an engine the package does not
// ship.
func TestEveryTestedPlatformIsServedOrRefused(t *testing.T) {
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
		if _, ok := p.Asset(arch); ok {
			continue
		}
		// No artifact for it, so routing has to say so rather than try.
		if reason := pinUncoveredIn(p, s.Platform, s.Backend); reason == "" {
			t.Errorf("%s/%s maps to arch %q, which has no engine in the pin, yet routing would send %s to it",
				s.OS, s.Arch, arch, s.Backend)
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
		{goos: "windows", goarch: "amd64", want: "win-x86_64"},
		// Apple silicon only: opencoti publishes nothing for an Intel Mac.
		{goos: "darwin", goarch: "arm64", want: "macos-aarch64"},
		{goos: "darwin", goarch: "amd64", wantErr: true},
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
	if got, want := p.URL(Asset{Path: "a.llamafile"}), "https://huggingface.co/o/r/resolve/main/a.llamafile"; got != want {
		t.Errorf("URL() = %q, want %q", got, want)
	}
	// A component's file is fetched from that component's commit.
	if got, want := p.URL(Asset{Path: "x", Repo: "a/b", Rev: "c"}), "https://huggingface.co/a/b/resolve/c/x"; got != want {
		t.Errorf("URL() = %q, want %q", got, want)
	}
}

// TestPinIsASCII guards the CMake parser, which is byte-oriented. CMake's
// regex "." does not match the bytes of a multi-byte UTF-8 character, so a
// single em-dash in a comment once left the tail of that comment being parsed
// as a statement. cmake/opencoti-fetch.cmake no longer depends on this for
// whole-line comments, but keeping the files ASCII removes the class.
func TestPinIsASCII(t *testing.T) {
	entries, err := pinFS.ReadDir(pinDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		text, _ := pinFS.ReadFile(pinDir + "/" + e.Name())
		for i, r := range string(text) {
			if r > 127 {
				t.Errorf("%s byte %d is %q (U+%04X); keep the pin ASCII for the CMake parser", e.Name(), i, r, r)
			}
		}
	}
}
