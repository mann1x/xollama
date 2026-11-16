package discover

import (
	"os"
	"strings"
	"testing"

	"github.com/ollama/ollama/ml"
)

func staleFreeDevices() []ml.DeviceInfo {
	return []ml.DeviceInfo{
		{DeviceID: ml.DeviceID{Library: "CUDA", ID: "0"}, PCIID: "0000:11:00.0", FreeMemory: 22 << 30},
		{DeviceID: ml.DeviceID{Library: "Vulkan", ID: "2"}, PCIID: "0000:03:00.0", FreeMemory: 15 << 30},
	}
}

// mails #2309/#2310: a runner's 0 for CUDA0 outlived the runner, because no
// refresh after it could reach the card.
func TestARunnersFreeMemoryDoesNotOutliveTheRunner(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "")
	t.Cleanup(func() { listedFree = map[string]uint64{} })
	listedFree = map[string]uint64{}
	devices := staleFreeDevices()
	noteListedFree(devices, nil) // the bootstrap

	// A refresh while the runner was loaded: it reported CUDA0.
	devices[0].FreeMemory = 0
	settleStaleFree(devices, []bool{true, true}, []bool{true, false})
	if devices[0].FreeMemory != 0 {
		t.Fatal("a loaded runner's own report must stand")
	}

	// The runner is gone and the next refresh could not list CUDA.
	settleStaleFree(devices, []bool{false, true}, []bool{false, false})
	if got := devices[0].FreeMemory; got != 22<<30 {
		t.Fatalf("CUDA0 free = %d, want its last listed 22 GiB", got)
	}
}

func TestAListingIsWhatAStaleDeviceReturnsTo(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "")
	t.Cleanup(func() { listedFree = map[string]uint64{} })
	listedFree = map[string]uint64{}
	devices := staleFreeDevices()
	noteListedFree(devices, nil)

	devices[0].FreeMemory = 20 << 30 // a later listing through the engine
	settleStaleFree(devices, []bool{true, true}, []bool{false, false})
	devices[0].FreeMemory = 1 << 30 // a runner's accounting, then the runner goes
	settleStaleFree(devices, []bool{false, true}, []bool{false, false})
	if got := devices[0].FreeMemory; got != 20<<30 {
		t.Fatalf("CUDA0 free = %d, want the latest listing's 20 GiB", got)
	}
}

func TestStockEngineKeepsUpstreamsStaleTable(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "llamacpp")
	t.Cleanup(func() { listedFree = map[string]uint64{} })
	listedFree = map[string]uint64{}
	devices := staleFreeDevices()
	noteListedFree(devices, nil)
	devices[0].FreeMemory = 0
	settleStaleFree(devices, []bool{false, true}, []bool{false, false})
	if devices[0].FreeMemory != 0 {
		t.Fatal("XOLLAMA_ENGINE=llamacpp must keep upstream's table")
	}
}

// The refresh calls live in upstream's runner.go; this fails if a sync drops
// one of them.
func TestTheRefreshSettlesStaleFreeMemory(t *testing.T) {
	src, err := os.ReadFile("runner.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []string{
		"noteListedFree(devices, nil) // xollama-hook: stale-free",
		"byRunner := slices.Clone(updated) // xollama-hook: stale-free",
		"settleStaleFree(devices, updated, byRunner) // xollama-hook: stale-free",
	} {
		if !strings.Contains(string(src), call) {
			t.Errorf("discover/runner.go lost %q", call)
		}
	}
}
