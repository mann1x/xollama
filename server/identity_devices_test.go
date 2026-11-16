package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/ml"
)

// The table reads a loaded model's GPU through its runner, as the scheduler
// does; without the runners it showed the card's free memory from before
// the load (eleven2go, 2026-11-16).
func TestTheDevicesRouteAsksTheLoadedRunners(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, runner := loadedNamed(t, "gemma4:latest", 0)
	restore := discoverDevices
	t.Cleanup(func() { discoverDevices = restore })
	var got []ml.FilteredRunnerDiscovery
	discoverDevices = func(_ context.Context, runners []ml.FilteredRunnerDiscovery) []ml.DeviceInfo {
		got = runners
		return solidPCDevices()
	}
	h, err := s.GenerateRoutes()
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, api.XollamaDevicesPath, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if len(got) != 1 || got[0] != ml.FilteredRunnerDiscovery(runner) {
		t.Fatalf("discovery got runners %v, want the loaded one", got)
	}

	s.sched.loadedMu.Lock()
	clear(s.sched.loaded)
	s.sched.loadedMu.Unlock()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, api.XollamaDevicesPath, nil))
	if got == nil || len(got) != 0 {
		t.Fatalf("with nothing loaded the route must pass an empty list, never nil (stale-free settles only then); got %#v", got)
	}
}

func TestTheDevicesRouteListsWhatAModelCanBePinnedTo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("XOLLAMA_ENGINE", "llamacpp")
	restore := discoverDevices
	t.Cleanup(func() { discoverDevices = restore })
	discoverDevices = func(context.Context, []ml.FilteredRunnerDiscovery) []ml.DeviceInfo { return solidPCDevices() }

	s := &Server{}
	h, err := s.GenerateRoutes()
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, api.XollamaDevicesPath, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var resp api.XollamaDevicesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Devices) != 2 {
		t.Fatalf("want both devices, the iGPU included — being pinnable is what it is listed for; got %+v", resp.Devices)
	}
	igpu := resp.Devices[1]
	if igpu.Backend != "Vulkan" || igpu.PCIID != "0000:18:00.0" || !igpu.Integrated || igpu.ID != "1" {
		t.Errorf("iGPU listed as %+v", igpu)
	}
	if igpu.Engine != "llamacpp" {
		t.Errorf("under XOLLAMA_ENGINE=llamacpp the serving engine is llamacpp, got %q", igpu.Engine)
	}
	if !slices.Contains(resp.Backends, "CPU") {
		t.Errorf("CPU is always a backend, got %v", resp.Backends)
	}
}
