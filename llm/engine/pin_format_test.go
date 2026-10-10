package engine

import (
	"strings"
	"testing"
)

// The engine is one file for every platform (`file any bin`), with a second
// row for the same bytes under the .exe name Windows needs. A platform takes
// its own row when there is one, else `any`.
func TestTheEngineIsItsPlatformsRowElseTheAnyRow(t *testing.T) {
	p := mustLoad(t, enginePin(
		row("any", "bin", "components/engine/v/opencoti-1", "engine"),
		row("win-x86_64", "bin", "components/engine/v/opencoti-1.exe", "engine"),
		row("any", "build-info", "components/engine/v/BUILD_INFO.engine.md", "info")))
	for arch, want := range map[string]string{
		"x86_64": "opencoti-1", "aarch64": "opencoti-1", ArchMacOS: "opencoti-1", "win-x86_64": "opencoti-1.exe",
	} {
		a, ok := p.Asset(arch)
		if !ok || a.StagedName() != want {
			t.Errorf("Asset(%s) = %+v, %v; want %s", arch, a, ok, want)
		}
		if n := len(p.Files(arch)); n != 2 {
			t.Errorf("Files(%s) has %d files, want the engine and its BUILD_INFO: %+v", arch, n, p.Files(arch))
		}
	}
	if p.Tag != "opencoti-1" || p.Version != fixEngine || p.Channel != ChannelDev || p.Repo != "o/r" || p.Rev != fixRev {
		t.Errorf("pin = %+v", p)
	}
}

// What a row becomes: a GPU library routes, anything else is a file the
// engine wants beside it -- a kind this build has never heard of included,
// which is how a new sidecar arrives without a parser change.
func TestFileKindsBecomeTheAssetsRoutingKnows(t *testing.T) {
	p := mustLoad(t,
		enginePin(row("any", "bin", "components/engine/v/opencoti-1", "engine"),
			"feature kv_status_v1", "feature images_generate_v1 x86_64 win-x86_64"),
		libPin("cuda", "sass 86 120", "requires x86_64 glibc 2.27",
			row("x86_64", "cuda", "components/cuda/v/ggml-cuda-x86_64.so", "cuda"),
			row("any", "build-info", "components/cuda/v/BUILD_INFO.cuda.md", "cuda info")),
		libPin("cuda12", "sass 52 61 70", "absent win-x86_64 not built",
			row("x86_64", "cuda12", "components/cuda12/v/ggml-cuda-cu12-x86_64.so", "cuda12")),
		libPin("vulkan", "gated-with "+fixEngine,
			row("win-x86_64", "vulkan", "components/vulkan/v/ggml-vulkan-win-x86_64.dll", "vk")),
		libPin("macos",
			row(ArchMacOS, "ape", "components/macos/v/ape-macos-aarch64", "loader"),
			row(ArchMacOS, "metal", "components/macos/v/ggml-metal-aarch64.dylib", "metal")),
		libPin("media",
			row("x86_64", "codec", "components/media/v/oc-codec-linux-x86_64.so", "codec"),
			row("x86_64", "espeak-data", "components/media/v/oc-espeak-data.bin", "a new kind"),
			row("any", "licence", "components/media/v/COPYING.x", "text", "for", "codec"),
			// A later column: extra trailing fields of a fixed KEY are ignored.
			row("aarch64", "codec", "components/media/v/oc-codec-linux-aarch64.so", "arm codec", "a-later-column")))

	if d, ok := p.DSO("x86_64"); !ok || d.StagedName() != "ggml-cuda-x86_64.so" || d.Component != "cuda" || d.Bytes != 4 {
		t.Errorf("DSO(x86_64) = %+v, %v", d, ok)
	}
	if d, ok := p.DSO("win-x86_64-vulkan"); !ok || d.StagedName() != "ggml-vulkan-win-x86_64.dll" {
		t.Errorf("DSO(win-x86_64-vulkan) = %+v, %v", d, ok)
	}
	if d, ok := p.CUDA12DSO("x86_64"); !ok || d.StagedName() != "ggml-cuda-cu12-x86_64.so" {
		t.Errorf("CUDA12DSO(x86_64) = %+v, %v", d, ok)
	}
	if _, ok := p.CUDA12DSO("win-x86_64"); ok {
		t.Error("the CUDA 12 library is stated absent on Windows and the pin has one")
	}
	for _, tt := range []struct {
		arch string
		b    Backend
		want bool
	}{
		{"x86_64", BackendCUDA, true},
		{"x86_64", BackendVulkan, false},
		{"win-x86_64", BackendVulkan, true},
		{"win-x86_64", BackendCUDA, false},
		{ArchMacOS, BackendMetal, true},
		{"aarch64", BackendCUDA, false},
		{"aarch64", BackendCPU, true},
	} {
		if got := p.Accelerates(tt.arch, tt.b); got != tt.want {
			t.Errorf("Accelerates(%s, %s) = %v, want %v", tt.arch, tt.b, got, tt.want)
		}
	}
	if len(p.CUDASASS) != 2 || p.CUDASASS[1] != 120 || len(p.CUDA12SASS) != 3 {
		t.Errorf("sass: cuda %v, cuda12 %v", p.CUDASASS, p.CUDA12SASS)
	}

	var roles []string
	for _, s := range p.Sidecars("x86_64") {
		roles = append(roles, s.Role+":"+s.StagedName()+":"+s.For)
	}
	want := "build-info:BUILD_INFO.cuda.md: codec:oc-codec-linux-x86_64.so: espeak-data:oc-espeak-data.bin: licence:COPYING.x:codec"
	if got := strings.Join(roles, " "); got != want {
		t.Errorf("x86_64 sidecars = %s\nwant %s", got, want)
	}
	// A component with no row for the platform itself is not taken there:
	// its BUILD_INFO alone is not a payload.
	for _, f := range p.Files("aarch64") {
		if f.Component == "cuda" {
			t.Errorf("aarch64 stages %s of the cuda component, which has nothing for it", f.StagedName())
		}
	}
	if u := p.URL(p.Sidecars("x86_64")[1]); u != "https://huggingface.co/o/r/resolve/"+fixRev+"/components/media/v/oc-codec-linux-x86_64.so" {
		t.Errorf("URL = %s", u)
	}

	if !p.HasFeature("kv_status_v1") || p.HasFeature("images_generate_v1") {
		t.Errorf("features = %v", p.Features)
	}
	if !p.HasFeatureOn("images_generate_v1", "win-x86_64") || p.HasFeatureOn("images_generate_v1", "aarch64") || !p.HasFeatureOn("kv_status_v1", "aarch64") {
		t.Error("a feature limited to some platforms is not read per platform")
	}
}

// A GPU library for a platform the pin ships no engine for accelerates
// nothing there.
func TestALibraryForAPlatformWithNoEngineIsNotAnAccelClaim(t *testing.T) {
	p := mustLoad(t,
		enginePin(row("x86_64", "bin", "components/engine/v/opencoti-1", "engine")),
		libPin("cuda", row("win-x86_64", "cuda", "components/cuda/v/ggml-cuda-win-x86_64.dll", "cuda")))
	if p.Accelerates("win-x86_64", BackendCUDA) || p.Accelerates("x86_64", BackendCUDA) {
		t.Errorf("accels = %v, want none", p.Accels)
	}
}

func TestLoadPinRefuses(t *testing.T) {
	bin := row("any", "bin", "components/engine/v/opencoti-1", "engine")
	lib := row("x86_64", "cuda", "components/cuda/v/ggml-cuda-x86_64.so", "cuda")
	replace := func(text, old, new string) string {
		if !strings.Contains(text, old) {
			panic("fixture has no " + old)
		}
		return strings.Replace(text, old, new, 1)
	}
	good := func() map[string]string { return pinFiles(enginePin(bin), libPin("cuda", lib)) }
	cases := map[string]func() map[string]string{
		// The vendored copy is the published one, or it is not a pin.
		"a vendored pin that is not the one the index names": func() map[string]string {
			f := good()
			f["cuda.txt"] += "# edited here\n"
			return f
		},
		"a library built against another interface": func() map[string]string {
			return pinFiles(enginePin(bin), replace(libPin("cuda", lib), fixGGML, strings.Repeat("9", 64)))
		},
		"a library built for a newer engine": func() map[string]string {
			return pinFiles(enginePin(bin), replace(libPin("cuda", lib), "engine-min "+fixEngine, "engine-min 2610050000001"))
		},
		"an index without the engine": func() map[string]string { return pinFiles(libPin("cuda", lib)) },
		"a pin file the index names and the tree does not hold": func() map[string]string {
			f := good()
			delete(f, "cuda.txt")
			return f
		},
		"an index naming another version": func() map[string]string {
			f := good()
			f["index.txt"] = replace(f["index.txt"], "version "+fixLib, "version 2610040000001")
			return f
		},
		// A pin that forgot to say must not read as a release, nor invent one.
		"no channel": func() map[string]string {
			f := good()
			f["index.txt"] = replace(f["index.txt"], "channel dev\n", "")
			return f
		},
		"a nonsense channel": func() map[string]string {
			f := good()
			f["index.txt"] = replace(f["index.txt"], "channel dev", "channel nightly")
			return f
		},
		"an unknown KEY in the index": func() map[string]string {
			f := good()
			f["index.txt"] += "banana x\n"
			return f
		},
		"an unknown KEY in a pin":    func() map[string]string { return pinFiles(enginePin(bin, "banana x")) },
		"a pin that is not format 2": func() map[string]string { return pinFiles(replace(enginePin(bin), "format 2", "format 3")) },
		// opencoti re-cuts a release in place under the same file names, so a
		// branch rev silently repoints the pin at bytes the sha256 rows reject.
		"a branch rev":          func() map[string]string { return pinFiles(replace(enginePin(bin), "rev "+fixRev, "rev main")) },
		"a short rev":           func() map[string]string { return pinFiles(replace(enginePin(bin), "rev "+fixRev, "rev 21462d1")) },
		"no file row":           func() map[string]string { return pinFiles(enginePin()) },
		"an engine with no bin": func() map[string]string { return pinFiles(enginePin(row("any", "build-info", "c/BUILD_INFO.md", "i"))) },
		"a short file row":      func() map[string]string { return pinFiles(enginePin("file any bin components/x " + sum("x"))) },
		"an uppercase sha256": func() map[string]string {
			return pinFiles(enginePin("file any bin components/x " + strings.Repeat("A", 64) + " 1"))
		},
		"a size that is no number": func() map[string]string { return pinFiles(enginePin("file any bin components/x " + sum("x") + " big")) },
		"an escaping path":         func() map[string]string { return pinFiles(enginePin(row("any", "bin", "../x", "engine"))) },
		"an absolute path":         func() map[string]string { return pinFiles(enginePin(row("any", "bin", "/x", "engine"))) },
		"an unknown platform":      func() map[string]string { return pinFiles(enginePin(row("riscv", "bin", "c/x", "engine"))) },
		"the same row twice":       func() map[string]string { return pinFiles(enginePin(bin, bin)) },
		"a sass that is no capability": func() map[string]string {
			return pinFiles(enginePin(bin), libPin("cuda", lib, "sass sm_86"))
		},
		"a library pin without engine-min": func() map[string]string {
			return pinFiles(enginePin(bin), replace(libPin("cuda", lib), "engine-min "+fixEngine+"\n", ""))
		},
		// One staging directory: two components cannot stage one name.
		"a file name two components share": func() map[string]string {
			return pinFiles(enginePin(bin, row("any", "build-info", "c/BUILD_INFO.md", "a")),
				libPin("cuda", lib, row("any", "build-info", "c/BUILD_INFO.md", "b")))
		},
	}
	if _, err := loadFiles(good()); err != nil {
		t.Fatalf("the fixture these cases break does not parse: %v", err)
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			if p, err := loadFiles(files()); err == nil {
				t.Errorf("LoadPin accepted it: %+v", p)
			}
		})
	}
}

// `sbsa absent` is how an index leaves a component out.
func TestAnAbsentComponentIsNotTaken(t *testing.T) {
	files := pinFiles(enginePin(row("any", "bin", "components/engine/v/opencoti-1", "engine")))
	files["index.txt"] += "sbsa absent\ncuda absent\n"
	p, err := loadFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	if p.Accelerates("x86_64", BackendCUDA) {
		t.Error("an absent cuda component accelerates CUDA")
	}
}

// The sbsa component is the CUDA library of Linux aarch64, with a sass list of
// its own: a Spark (12.1) is covered there and a Jetson Orin (8.7) is not,
// whatever the x86_64 library carries.
func TestTheSBSAComponentServesAarch64WithItsOwnSASS(t *testing.T) {
	bin := row("any", "bin", "components/engine/v/opencoti-1", "engine")
	files := pinFiles(enginePin(bin),
		libPin("cuda", row("x86_64", "cuda", "c/ggml-cuda-x86_64.so", "cuda"), "sass 75 80 86 89 90 120"),
		libPin("sbsa", row("aarch64", "cuda", "c/ggml-cuda-sbsa-aarch64.so", "sbsa"), "sass 110 121"))
	p, err := loadFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Accelerates("aarch64", BackendCUDA) {
		t.Error("the sbsa library does not accelerate CUDA on aarch64")
	}
	if a, ok := p.DSO("aarch64"); !ok || a.StagedName() != "ggml-cuda-sbsa-aarch64.so" {
		t.Errorf("aarch64 CUDA library = %+v, %v", a, ok)
	}
	for _, c := range []struct {
		arch         string
		major, minor int
		want         bool
	}{
		{"aarch64", 12, 1, true},
		{"aarch64", 11, 0, true},
		{"aarch64", 11, 2, true},
		{"aarch64", 8, 7, false},
		{"aarch64", 12, 0, false},
		{"aarch64", 12, 2, false},
		{"x86_64", 12, 1, true},
		{"x86_64", 12, 2, true},
		{"x86_64", 8, 7, true},
		{"x86_64", 11, 0, false},
	} {
		if got := p.CoversCUDAOn(c.arch, c.major, c.minor); got != c.want {
			t.Errorf("on %s CoversCUDA(%d.%d) = %v, want %v", c.arch, c.major, c.minor, got, c.want)
		}
	}
	// The CUDA 12 payload exists for Linux x86_64 only: no other host has it.
	// And the answer follows the platform asked about, on any machine.
	arm, x86 := Platform{OS: "linux", Arch: "arm64"}, Platform{OS: "linux", Arch: "amd64"}
	spark := Device{Backend: BackendCUDA, ComputeMajor: 12, ComputeMinor: 1}
	orin := Device{Backend: BackendCUDA, ComputeMajor: 8, ComputeMinor: 7}
	volta := Device{Backend: BackendCUDA, ComputeMajor: 7, ComputeMinor: 0}
	if !coveredByCUDA13(p, arm, spark) || coveredByCUDA13(p, arm, orin) || !coveredByCUDA13(p, x86, orin) {
		t.Error("coveredByCUDA13 does not follow the platform it is asked about")
	}
	// The committed pin has the cuda12 component this test's pin leaves out.
	committed, err := DefaultPin()
	if err != nil {
		t.Fatal(err)
	}
	if needsCUDA12(committed, arm, volta) || !needsCUDA12(committed, x86, volta) {
		t.Error("needsCUDA12 does not follow the platform it is asked about")
	}
	if p.CoversCUDA12On("aarch64", 7, 0) {
		t.Error("an aarch64 host is offered a CUDA 12 payload it has no file for")
	}
}
