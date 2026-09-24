package engine

import (
	"slices"
	"testing"
)

func TestOffMeansOpencotiEnumeratesNothing(t *testing.T) {
	for _, b := range []Backend{BackendCUDA, BackendVulkan, BackendCPU} {
		if Enumerates(Platform{OS: "linux", Arch: "amd64"}, b, "llamacpp") {
			t.Errorf("XOLLAMA_ENGINE=llamacpp still enumerates %s through opencoti", b)
		}
	}
}

func TestForcedOpencotiEnumeratesEvenUntested(t *testing.T) {
	if !Enumerates(Platform{OS: "linux", Arch: "amd64"}, BackendROCm, "opencoti") {
		t.Error("a forced engine must enumerate what it will be asked to run on")
	}
}

func TestAutoEnumeratesOnlyWhatThePinCovers(t *testing.T) {
	restore := loadPin
	t.Cleanup(func() { loadPin = restore })
	loadPin = func() (Pin, error) {
		return Pin{
			Tag:     "t",
			Channel: "dev",
			Accels:  []Accel{{Arch: "x86_64", Backend: BackendCUDA}},
			Assets:  []Asset{{Kind: "bin", Arch: "x86_64"}},
		}, nil
	}
	host := Platform{OS: "linux", Arch: "amd64"}
	if !Enumerates(host, BackendCUDA, "") {
		t.Error("CUDA is covered by the pin and tested, so opencoti must enumerate it")
	}
	if Enumerates(host, BackendVulkan, "auto") {
		t.Error("a pin with no Vulkan payload routes Vulkan to llama.cpp, so llama.cpp must enumerate it")
	}
	if Enumerates(host, BackendROCm, "") {
		t.Error("ROCm is untested, so opencoti must not enumerate it")
	}
}

func TestEnumerateCommandSelectsOneBackendAndReachesTheServerPath(t *testing.T) {
	name, args := EnumerateCommand("/lib/ollama/opencoti", BackendVulkan, "linux")
	if name != "sh" || args[0] != "/lib/ollama/opencoti" {
		t.Fatalf("an APE is run through sh on linux, got %q %q", name, args)
	}
	for _, want := range []string{"--server", "--list-devices", "-m"} {
		if !slices.Contains(args, want) {
			t.Errorf("missing %s in %q", want, args)
		}
	}
	if i := slices.Index(args, "--gpu"); i < 0 || args[i+1] != "vulkan" {
		t.Errorf("want --gpu vulkan, got %q", args)
	}
	name, args = EnumerateCommand(`C:\x\opencoti.exe`, BackendCUDA, "windows")
	if name != `C:\x\opencoti.exe` || args[len(args)-1] != "nvidia" {
		t.Errorf("windows runs the artifact directly with --gpu nvidia, got %q %q", name, args)
	}
}
