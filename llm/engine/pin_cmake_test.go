package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// cmakeFixture lays a pin directory and the files it names out on disk, and
// returns a function that runs the real fetch script against them for one
// platform: the engine from -DLOCAL_FILE, everything else from
// -DLOCAL_SIDECAR_DIR, so nothing is downloaded.
type cmakeFixture struct {
	t        *testing.T
	pin, src string
	files    map[string]string
}

func newCMakeFixture(t *testing.T, bodies map[string]string, comps ...string) *cmakeFixture {
	t.Helper()
	if _, err := exec.LookPath("cmake"); err != nil {
		t.Skip("cmake is not installed")
	}
	f := &cmakeFixture{t: t, pin: t.TempDir(), src: t.TempDir(), files: pinFiles(comps...)}
	for name, text := range f.files {
		f.write(f.pin, name, text)
	}
	for name, body := range bodies {
		f.write(f.src, name, body)
	}
	return f
}

func (f *cmakeFixture) write(dir, name, body string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *cmakeFixture) run(arch, dest string, more ...string) (string, error) {
	f.t.Helper()
	script, err := filepath.Abs("../../cmake/opencoti-fetch.cmake")
	if err != nil {
		f.t.Fatal(err)
	}
	args := append([]string{
		"-DPIN_DIR=" + f.pin, "-DARCH=" + arch, "-DDEST_DIR=" + dest,
		"-DLOCAL_FILE=" + filepath.Join(f.src, "engine"), "-DLOCAL_SIDECAR_DIR=" + f.src,
	}, more...)
	out, err := exec.Command("cmake", append(args, "-P", script)...).CombinedOutput()
	return string(out), err
}

// cmakeBodies are the bytes behind cmakeComps' rows, by published file name.
var cmakeBodies = map[string]string{
	"engine":                       "the engine",
	"ggml-cuda-x86_64.so":          "cuda 13",
	"ggml-cuda-cu12-x86_64.so":     "cuda 12",
	"ggml-vulkan-win-x86_64.dll":   "vulkan",
	"ape-macos-aarch64":            "the loader",
	"ggml-metal-aarch64.dylib":     "metal",
	"oc-codec-linux-x86_64.so":     "the codec",
	"oc-codec-win-x86_64.dll":      "the windows codec",
	"oc-codec-macos-aarch64.dylib": "the mac codec",
	"oc-new-kind-linux-x86_64.so":  "a kind nobody has listed",
	"COPYING.codec":                "a licence",
	"BUILD_INFO.cuda.md":           "cuda info",
	"BUILD_INFO.media.md":          "media info",
}

func cmakeComps() []string {
	b := cmakeBodies
	return []string{
		// The index lists the engine last: the order is not the format's.
		libPin("cuda", "sass 86 120", "requires x86_64 glibc 2.27",
			row("x86_64", "cuda", "components/cuda/v/ggml-cuda-x86_64.so", b["ggml-cuda-x86_64.so"]),
			row("any", "build-info", "components/cuda/v/BUILD_INFO.md", b["BUILD_INFO.cuda.md"])),
		libPin("cuda12", "sass 70", "absent win-x86_64 not built",
			row("x86_64", "cuda12", "components/cuda12/v/ggml-cuda-cu12-x86_64.so", b["ggml-cuda-cu12-x86_64.so"])),
		libPin("vulkan", row("win-x86_64", "vulkan", "components/vulkan/v/ggml-vulkan-win-x86_64.dll", b["ggml-vulkan-win-x86_64.dll"])),
		libPin("macos",
			row(ArchMacOS, "ape", "components/macos/v/ape-macos-aarch64", b["ape-macos-aarch64"]),
			row(ArchMacOS, "metal", "components/macos/v/ggml-metal-aarch64.dylib", b["ggml-metal-aarch64.dylib"])),
		libPin("media", "gated-with "+fixEngine,
			row("x86_64", "codec", "components/media/v/oc-codec-linux-x86_64.so", b["oc-codec-linux-x86_64.so"]),
			row("win-x86_64", "codec", "components/media/v/oc-codec-win-x86_64.dll", b["oc-codec-win-x86_64.dll"]),
			row(ArchMacOS, "codec", "components/media/v/oc-codec-macos-aarch64.dylib", b["oc-codec-macos-aarch64.dylib"]),
			row("x86_64", "new-kind", "components/media/v/oc-new-kind-linux-x86_64.so", b["oc-new-kind-linux-x86_64.so"]),
			row("any", "licence", "components/media/v/COPYING.codec", b["COPYING.codec"], "for", "codec"),
			row("any", "build-info", "components/media/v/BUILD_INFO.md", b["BUILD_INFO.media.md"])),
		enginePin("feature kv_status_v1", "feature images_generate_v1 x86_64 win-x86_64",
			row("any", "bin", "components/engine/v/opencoti-1", b["engine"]),
			row("win-x86_64", "bin", "components/engine/v/opencoti-1.exe", b["engine"])),
	}
}

// TestCMakeStagesWhatGoReads runs the real fetch script (the one the CMake
// build, the container image and every release stage with) against a pin this
// parser reads too. For every platform, what Go says the pin carries is what
// CMake staged: the same files under the same names, the engine and the macOS
// loader executable and nothing else, and no other platform's file.
func TestCMakeStagesWhatGoReads(t *testing.T) {
	f := newCMakeFixture(t, cmakeBodies, cmakeComps()...)
	p, err := loadFiles(f.files)
	if err != nil {
		t.Fatalf("the Go parser refuses the pin CMake is given: %v", err)
	}
	for _, arch := range platforms {
		dest := t.TempDir()
		manifest := filepath.Join(t.TempDir(), "manifest")
		if out, err := f.run(arch, dest, "-DMANIFEST="+manifest); err != nil {
			t.Fatalf("%s: cmake: %v\n%s", arch, err, out)
		}
		var want []string
		for _, a := range p.Files(arch) {
			want = append(want, a.StagedName())
			fi, err := os.Stat(filepath.Join(dest, a.StagedName()))
			if err != nil {
				t.Errorf("%s: %s was not staged beside the engine", arch, a.StagedName())
				continue
			}
			runs := a.Kind == kindBin || a.Role == SidecarAPE
			if got := fi.Mode()&0o111 != 0; got != runs {
				t.Errorf("%s: %s staged with mode %v, executable = %v, want %v", arch, a.StagedName(), fi.Mode(), got, runs)
			}
		}
		var staged []string
		entries, _ := os.ReadDir(dest)
		for _, e := range entries {
			staged = append(staged, e.Name())
		}
		slices.Sort(want)
		slices.Sort(staged)
		if !slices.Equal(staged, want) {
			t.Errorf("%s: CMake staged %v\n                  Go reads %v", arch, staged, want)
		}
		// The manifest names every staged file, for the scripts that check a
		// payload against the pin.
		lines, _ := os.ReadFile(manifest)
		if n := strings.Count(string(lines), "\n"); n != len(want) {
			t.Errorf("%s: the manifest has %d lines for %d files:\n%s", arch, n, len(want), lines)
		}
	}
	// The platforms differ, or the comparison above proves nothing.
	if x, w := len(p.Files("x86_64")), len(p.Files("aarch64")); x != 8 || w != 1 {
		t.Fatalf("Go reads %d files for x86_64 and %d for aarch64, want 8 and 1", x, w)
	}
}

func TestCMakeRefuses(t *testing.T) {
	cases := map[string]struct {
		break_ func(f *cmakeFixture)
		says   string
	}{
		"the wrong bytes under the right name": {
			func(f *cmakeFixture) { f.write(f.src, "oc-codec-linux-x86_64.so", "tampered!") },
			"does not match the pin",
		},
		"the wrong engine": {
			func(f *cmakeFixture) { f.write(f.src, "engine", "another cut") },
			"does not match the pin",
		},
		"a file missing from an offline build": {
			func(f *cmakeFixture) { os.Remove(filepath.Join(f.src, "COPYING.codec")) },
			"has no COPYING.codec",
		},
		"a vendored pin that is not the one the index names": {
			func(f *cmakeFixture) { f.write(f.pin, "media.txt", f.files["media.txt"]+"# edited here\n") },
			"is not the pin the index names",
		},
		"an unknown KEY": {
			func(f *cmakeFixture) {
				f.write(f.pin, "index.txt", f.files["index.txt"]+"banana x\n")
			},
			"unknown KEY",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newCMakeFixture(t, cmakeBodies, cmakeComps()...)
			tc.break_(f)
			out, err := f.run("x86_64", t.TempDir())
			if err == nil || !strings.Contains(out, tc.says) {
				t.Fatalf("cmake staged it, or refused for another reason (want %q): %v\n%s", tc.says, err, out)
			}
			if _, err := loadFiles(f.files); err != nil {
				t.Fatalf("the unbroken fixture does not parse: %v", err)
			}
		})
	}
}

// The two refusals the format asks of a consumer's build, in both parsers: a
// library whose abi the engine does not provide, and one built for a newer
// engine than the index takes.
func TestCMakeAndGoRefuseAnIncompatibleComponent(t *testing.T) {
	bin := row("any", "bin", "components/engine/v/opencoti-1", cmakeBodies["engine"])
	lib := row("x86_64", "cuda", "components/cuda/v/ggml-cuda-x86_64.so", cmakeBodies["ggml-cuda-x86_64.so"])
	for name, tc := range map[string]struct{ cuda, says string }{
		"another abi":    {strings.Replace(libPin("cuda", lib), fixGGML, strings.Repeat("9", 64), 1), "needs abi"},
		"a newer engine": {strings.Replace(libPin("cuda", lib), "engine-min "+fixEngine, "engine-min 2610050000001", 1), "or newer"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCMakeFixture(t, cmakeBodies, enginePin(bin), tc.cuda)
			if _, err := loadFiles(f.files); err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Errorf("Go: %v, want a refusal saying %q", err, tc.says)
			}
			if out, err := f.run("x86_64", t.TempDir()); err == nil || !strings.Contains(out, tc.says) {
				t.Errorf("cmake: %v, want a refusal saying %q\n%s", err, tc.says, out)
			}
		})
	}
}

// A library an earlier pin staged under another name is one the engine may
// load instead of the pinned one, and nothing would say so.
func TestCMakeRemovesWhatTheClassicPinStaged(t *testing.T) {
	f := newCMakeFixture(t, cmakeBodies, cmakeComps()...)
	dest := t.TempDir()
	f.write(dest, "ggml-cuda.so", "an older CUDA library")
	f.write(dest, "libggml-cuda.so", "llama.cpp's own")
	if err := os.MkdirAll(filepath.Join(dest, "engines", "cuda_v12"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.write(filepath.Join(dest, "engines", "cuda_v12"), "opencoti-0", "a second engine")
	if out, err := f.run("x86_64", dest); err != nil {
		t.Fatalf("cmake: %v\n%s", err, out)
	}
	for name, stays := range map[string]bool{"ggml-cuda.so": false, "engines/cuda_v12": false, "libggml-cuda.so": true, "ggml-cuda-x86_64.so": true} {
		if _, err := os.Stat(filepath.Join(dest, name)); (err == nil) != stays {
			t.Errorf("%s: present = %v, want %v", name, err == nil, stays)
		}
	}
}
