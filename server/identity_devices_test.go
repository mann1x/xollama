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
