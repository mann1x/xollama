package discover

import (
	"context"
	"testing"
	"time"

	"github.com/ollama/ollama/llm/engine"
	"github.com/ollama/ollama/ml"
)

// refreshDevices is solidPC as a refresh sees it: the 3090 from the CUDA
// library, the Renoir iGPU from the Vulkan one, both with values from the
// last discovery.
func refreshDevices() []ml.DeviceInfo {
	d := solidPCLlamaCppDevices()
	d[0].LibraryPath = []string{ml.LibOllamaPath, "/lib/ollama/cuda_v13"}
	d[0].FreeMemory = 1000 * mib // stale: a model was loaded when it was read
	d[1].LibraryPath = []string{ml.LibOllamaPath, "/lib/ollama/vulkan"}
	return d
}

func withRefreshClock(t *testing.T, now time.Time) *time.Time {
	t.Helper()
	restoreNow, restoreSkip := refreshNow, refreshSkippedUntil
	t.Cleanup(func() { refreshNow, refreshSkippedUntil = restoreNow, restoreSkip })
	refreshSkippedUntil = map[string]time.Time{}
	clock := now
	refreshNow = func() time.Time { return clock }
	return &clock
}

// TestOffMeansOffForTheRefresh: under llamacpp the refresh is upstream's --
// nothing is listed through opencoti, no directory is skipped, nothing is
// remembered.
func TestOffMeansOffForTheRefresh(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "llamacpp")
	withRefreshClock(t, time.Unix(0, 0))
	ran := false
	restore := opencotiListDevices
	t.Cleanup(func() { opencotiListDevices = restore })
	opencotiListDevices = func(context.Context, string, engine.Backend) (string, error) { ran = true; return "", nil }

	devices := refreshDevices()
	updated := make([]bool, len(devices))
	f := forkRefresh(context.Background(), devices, updated)
	if f != nil || ran || updated[0] || updated[1] {
		t.Fatalf("llamacpp must refresh as upstream does: state %v, opencoti ran %v, updated %v", f, ran, updated)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.ran("/lib/ollama/vulkan", ctx, 0)
	if f.skip("/lib/ollama/vulkan", devices, updated) || f.skip("/lib/ollama/cuda_v13", devices, updated) {
		t.Error("upstream's refresh skips no directory")
	}
}

// TestTheEngineRefreshesTheDevicesItServes is bug-117: the 3090 is read back
// through opencoti, and its llama.cpp directory is not started at all.
func TestTheEngineRefreshesTheDevicesItServes(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")
	withRefreshClock(t, time.Unix(0, 0))
	withOpencoti(t, map[engine.Backend]string{engine.BackendCUDA: solidPCCUDAListing})

	devices := refreshDevices()
	updated := make([]bool, len(devices))
	f := forkRefresh(context.Background(), devices, updated)
	if !updated[0] || devices[0].FreeMemory != 23853*mib {
		t.Fatalf("the 3090 must be refreshed from opencoti's listing: updated %v, free %d MiB", updated[0], devices[0].FreeMemory/mib)
	}
	if updated[1] {
		t.Error("the iGPU is llama.cpp's here (no Vulkan listing) and must still be waiting")
	}
	if !f.skip("/lib/ollama/cuda_v13", devices, updated) {
		t.Error("a directory whose devices are all refreshed must not be started")
	}
	if f.skip("/lib/ollama/vulkan", devices, updated) {
		t.Error("a directory with a device still waiting must be tried")
	}
}

// TestADirectoryThatRanOutOfTimeWaitsItsTurn: stale either way, so it is not
// waited for again until the cooldown is over.
func TestADirectoryThatRanOutOfTimeWaitsItsTurn(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")
	clock := withRefreshClock(t, time.Unix(1000, 0))
	withOpencoti(t, map[engine.Backend]string{})

	devices := refreshDevices()
	updated := make([]bool, len(devices))
	f := forkRefresh(context.Background(), devices, updated)

	expired, cancel := context.WithCancel(context.Background())
	cancel()
	f.ran("/lib/ollama/vulkan", expired, 0)
	if !f.skip("/lib/ollama/vulkan", devices, updated) {
		t.Fatal("a directory that ran out of time must be skipped on the next refresh")
	}
	*clock = clock.Add(refreshCooldown + time.Second)
	if f.skip("/lib/ollama/vulkan", devices, updated) {
		t.Error("after the cooldown the directory must be tried again")
	}

	// Finding devices, or finishing in time, is not a timeout.
	f.ran("/lib/ollama/vulkan", expired, 1)
	f.ran("/lib/ollama/vulkan", context.Background(), 0)
	if f.skip("/lib/ollama/vulkan", devices, updated) {
		t.Error("only a refresh that ran out of time and found nothing goes into cooldown")
	}
}

// TestAnEngineListingThatRanOutOfTimeWaitsItsTurn: a cold CUDA init can take
// longer than the whole budget; it is not retried on every load either.
func TestAnEngineListingThatRanOutOfTimeWaitsItsTurn(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "opencoti")
	withRefreshClock(t, time.Unix(0, 0))
	calls := map[engine.Backend]int{}
	withOpencoti(t, nil)
	opencotiListDevices = func(_ context.Context, _ string, b engine.Backend) (string, error) {
		calls[b]++
		return "", context.DeadlineExceeded
	}

	expired, cancel := context.WithCancel(context.Background())
	cancel()
	for range 3 {
		devices := refreshDevices()
		forkRefresh(expired, devices, make([]bool, len(devices)))
	}
	for b, n := range calls {
		if n != 1 {
			t.Errorf("opencoti was asked for %s %d times, want once and then a cooldown", b, n)
		}
	}
	if len(calls) == 0 {
		t.Fatal("opencoti was never asked")
	}
}
