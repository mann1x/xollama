package discover

import (
	"context"
	"testing"

	"github.com/ollama/ollama/llm/engine"
	"github.com/ollama/ollama/ml"
)

const v100Listing = `ggml_cuda_init: found 1 CUDA devices (Total VRAM: 32494 MiB):
  Device 0: Tesla V100-SXM2-32GB, compute capability 7.0, VMM: yes, VRAM: 32494 MiB
Available devices:
  CUDA0: Tesla V100-SXM2-32GB (32494 MiB, 32100 MiB free)
`

// The V100 tester's case: the engine's own pick, CUDA 13, lists nothing on a
// Volta card, and the same engine told to load its CUDA 12 library lists it.
// The V100 must survive discovery, and the refresh asks only for the library
// that listed it.
func TestAV100ListedOnlyByTheCUDA12LibraryIsKept(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")
	restoreList, restoreFind, restore12, restoreHas := opencotiListDevices, opencotiArtifact, opencotiListLegacyCUDA, opencotiHasCUDA12
	t.Cleanup(func() {
		opencotiListDevices, opencotiArtifact, opencotiListLegacyCUDA, opencotiHasCUDA12 = restoreList, restoreFind, restore12, restoreHas
		cudaLister.known, cudaLister.legacy = false, false
	})
	onLinux(t)
	cudaLister.known, cudaLister.legacy = false, false
	opencotiArtifact = func() (string, error) { return "/lib/ollama/opencoti", nil }
	opencotiHasCUDA12 = func(string) bool { return true }
	var picked, legacy int
	opencotiListDevices = func(_ context.Context, _ string, b engine.Backend) (string, error) {
		if b != engine.BackendCUDA {
			return "", nil
		}
		picked++
		return "ggml_cuda_init: no CUDA devices found\n", nil
	}
	opencotiListLegacyCUDA = func(_ context.Context, artifact string) (string, error) {
		if artifact != "/lib/ollama/opencoti" {
			t.Errorf("the CUDA 12 listing asked %s; there is one engine", artifact)
		}
		legacy++
		return v100Listing, nil
	}
	v100 := []ml.DeviceInfo{{
		DeviceID: ml.DeviceID{ID: "0", Library: "CUDA"}, Description: "Tesla V100-SXM2-32GB",
		PCIID: "0000:00:1e.0", TotalMemory: 32494 * mib, FreeMemory: 32000 * mib, ComputeMajor: 7, ComputeMinor: 0,
	}}

	cuda := byLibrary(overlayOpencotiDevices(context.Background(), v100), "CUDA")
	if len(cuda) != 1 || cuda[0].ComputeMajor != 7 || cuda[0].FreeMemory != 32100*mib {
		t.Fatalf("the V100 must stay, with the CUDA 12 library's free memory; got %+v", cuda)
	}
	if _, err := opencotiListing(context.Background(), "/lib/ollama/opencoti", engine.BackendCUDA); err != nil {
		t.Fatal(err)
	}
	if picked != 1 || legacy != 2 {
		t.Errorf("the refresh must ask only for the library that listed the V100: own pick %d, CUDA 12 %d", picked, legacy)
	}
}

// A pin without a CUDA 12 library, or an engine staged without it, is asked
// once: there is no second library to list with.
func TestNoCUDA12LibraryMeansOneListing(t *testing.T) {
	restoreList, restore12, restoreHas := opencotiListDevices, opencotiListLegacyCUDA, opencotiHasCUDA12
	t.Cleanup(func() {
		opencotiListDevices, opencotiListLegacyCUDA, opencotiHasCUDA12 = restoreList, restore12, restoreHas
		cudaLister.known, cudaLister.legacy = false, false
	})
	cudaLister.known, cudaLister.legacy = false, false
	opencotiHasCUDA12 = func(string) bool { return false }
	opencotiListDevices = func(context.Context, string, engine.Backend) (string, error) { return v100Listing, nil }
	opencotiListLegacyCUDA = func(context.Context, string) (string, error) {
		t.Error("the CUDA 12 listing ran with no CUDA 12 library staged")
		return "", nil
	}
	if got, err := opencotiListing(context.Background(), "/lib/ollama/opencoti", engine.BackendCUDA); err != nil || len(got) != 1 {
		t.Fatalf("listing = %+v, %v", got, err)
	}
}
