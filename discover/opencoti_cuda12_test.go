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

// The V100 tester's case: the main engine's CUDA 13 payload lists nothing on
// a Volta card, and the CUDA 12 engine beside its own payload lists it. The
// V100 must survive discovery, and the refresh asks only the engine that
// listed it.
func TestAV100ListedOnlyByTheCUDA12PayloadIsKept(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")
	restoreList, restoreFind, restore12 := opencotiListDevices, opencotiArtifact, opencotiCUDA12Artifact
	t.Cleanup(func() {
		opencotiListDevices, opencotiArtifact, opencotiCUDA12Artifact = restoreList, restoreFind, restore12
		cudaLister.artifact = ""
	})
	cudaLister.artifact = ""
	opencotiArtifact = func() (string, error) { return "/lib/ollama/opencoti", nil }
	opencotiCUDA12Artifact = func() (string, error) { return "/lib/ollama/engines/cuda_v12/opencoti", nil }
	asked := map[string]int{}
	opencotiListDevices = func(_ context.Context, artifact string, b engine.Backend) (string, error) {
		if b != engine.BackendCUDA {
			return "", nil
		}
		asked[artifact]++
		if artifact == "/lib/ollama/engines/cuda_v12/opencoti" {
			return v100Listing, nil
		}
		return "ggml_cuda_init: no CUDA devices found\n", nil
	}
	v100 := []ml.DeviceInfo{{
		DeviceID: ml.DeviceID{ID: "0", Library: "CUDA"}, Description: "Tesla V100-SXM2-32GB",
		PCIID: "0000:00:1e.0", TotalMemory: 32494 * mib, FreeMemory: 32000 * mib, ComputeMajor: 7, ComputeMinor: 0,
	}}

	cuda := byLibrary(overlayOpencotiDevices(context.Background(), v100), "CUDA")
	if len(cuda) != 1 || cuda[0].ComputeMajor != 7 || cuda[0].FreeMemory != 32100*mib {
		t.Fatalf("the V100 must stay, with the CUDA 12 engine's free memory; got %+v", cuda)
	}
	if _, err := opencotiListing(context.Background(), "/lib/ollama/opencoti", engine.BackendCUDA); err != nil {
		t.Fatal(err)
	}
	if asked["/lib/ollama/opencoti"] != 1 || asked["/lib/ollama/engines/cuda_v12/opencoti"] != 2 {
		t.Errorf("the refresh must ask only the engine that listed the V100: %v", asked)
	}
}
