package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/internal/systel"
	"github.com/ollama/ollama/ml"
)

type pidLlm struct {
	*mockLlm
	pid int
}

func (p pidLlm) Pid() int { return p.pid }

func systemRoute(t *testing.T, s *Server, snap systel.Snapshot, known []ml.DeviceInfo) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	restoreSnap, restoreKnown, restoreDiscover := systemSnapshot, knownDevices, discoverDevices
	t.Cleanup(func() { systemSnapshot, knownDevices, discoverDevices = restoreSnap, restoreKnown, restoreDiscover })
	systemSnapshot = func() systel.Snapshot { return snap }
	knownDevices = func() []ml.DeviceInfo { return known }
	discoverDevices = func(context.Context, []ml.FilteredRunnerDiscovery) []ml.DeviceInfo {
		t.Error("the telemetry route ran a discovery")
		return nil
	}
	h, err := s.GenerateRoutes()
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, api.XollamaSystemPath, nil))
	return w
}

func u64(v uint64) *uint64 { return &v }

// The route joins what the driver reads to the server's own view of each GPU,
// by bus address, and never runs a discovery to get that view.
func TestTheSystemRouteJoinsTheDriversReadingsToTheServersDevices(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "llamacpp")
	snap := systel.Snapshot{
		At: time.Unix(1_800_000_000, 0),
		Devices: []api.SystemDevice{
			{PCIID: "0000:01:00.0", Vendor: "nvidia", Name: "NVIDIA GeForce RTX 3090", Source: "nvml", TotalMemory: u64(24 << 30)},
			{PCIID: "0000:18:00.0", Vendor: "amd", Source: "sysfs"},
			{PCIID: "0000:42:00.0", Vendor: "amd", Source: "sysfs"}, // not one the server uses
		},
	}
	w := systemRoute(t, &Server{}, snap, solidPCDevices())
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var resp api.SystemResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if !resp.AsOf.Equal(snap.At) || resp.IntervalMS != 2000 {
		t.Errorf("as_of %v interval %d", resp.AsOf, resp.IntervalMS)
	}
	if len(resp.Devices) != 3 {
		t.Fatalf("every GPU the driver lists is reported, used or not: %+v", resp.Devices)
	}
	if d := resp.Devices[0]; d.Backend != "CUDA" || d.ID != "0" || d.Engine != "llamacpp" {
		t.Errorf("3090 joined as %+v", d)
	}
	if d := resp.Devices[1]; d.Backend != "Vulkan" || d.ID != "1" || !d.Integrated || d.Name != "AMD RADV RENOIR (ACO)" {
		t.Errorf("iGPU joined as %+v", d)
	}
	if d := resp.Devices[2]; d.Backend != "" || d.ID != "" {
		t.Errorf("a GPU the server does not use has no server view: %+v", d)
	}
	if resp.Models == nil || resp.Devices == nil {
		t.Error("empty lists are [] on the wire, not null")
	}
}

// A unified-memory GPU reports only what is in use: its size is the working
// set the server already plans against.
func TestUnifiedMemoryTakesTheServersWorkingSet(t *testing.T) {
	snap := systel.Snapshot{Devices: []api.SystemDevice{{Vendor: "apple", Source: "iokit", UsedMemory: u64(3 << 30)}}}
	known := []ml.DeviceInfo{{DeviceID: ml.DeviceID{ID: "0", Library: "Metal"}, Description: "Apple M4", TotalMemory: 11 << 30}}
	resp := (&Server{}).systemResponse(snap, known)
	d := resp.Devices[0]
	if d.Backend != "Metal" || d.TotalMemory == nil || *d.TotalMemory != 11<<30 || d.FreeMemory == nil || *d.FreeMemory != 8<<30 {
		t.Errorf("Apple GPU as %+v", d)
	}
}

// A runner still loading holds its lock for the whole load; the route skips
// it rather than wait, and names each runner process by its model.
func TestTheSystemRouteNeverWaitsOnALoad(t *testing.T) {
	ready := &runnerRef{
		llama: pidLlm{&mockLlm{totalSize: 6 << 30, vramSize: 6 << 30, vramByGPU: map[ml.DeviceID]uint64{{ID: "0", Library: "CUDA"}: 6 << 30}}, 4242},
		gpus:  []ml.DeviceID{{ID: "0", Library: "CUDA"}},
	}
	ready.model.Store(&Model{ShortName: "qwen3:8b"})
	loading := &runnerRef{llama: pidLlm{&mockLlm{}, 4343}}
	loading.model.Store(&Model{ShortName: "gemma4:e4b"})
	loading.refMu.Lock()
	defer loading.refMu.Unlock()
	s := &Server{sched: &Scheduler{loaded: map[string]*runnerRef{"a": ready, "b": loading}}}

	snap := systel.Snapshot{Server: api.SystemServer{PID: 1, Children: []api.SystemProcess{{PID: 4242, Name: "llama-server"}, {PID: 4343, Name: "llama-server"}}}}
	done := make(chan api.SystemResponse, 1)
	go func() { done <- s.systemResponse(snap, solidPCDevices()) }()
	var resp api.SystemResponse
	select {
	case resp = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the route waited on a loading runner")
	}
	if len(resp.Models) != 1 || resp.Models[0].Name != "qwen3:8b" || resp.Models[0].PID != 4242 {
		t.Fatalf("models %+v", resp.Models)
	}
	if d := resp.Models[0].Devices; len(d) != 1 || d[0].PCIID != "0000:01:00.0" || d[0].VRAM != 6<<30 {
		t.Errorf("placement %+v", d)
	}
	if c := resp.Server.Children; c[0].Model != "qwen3:8b" || c[1].Model != "" {
		t.Errorf("children %+v", c)
	}
}

// A poll reports the last link probe's measurement and never measures.
func TestTheLastLinkProbeIsReportedNotRepeated(t *testing.T) {
	linkMu.Lock()
	saved := linkMeasured
	linkMeasured = map[string]measuredLink{}
	linkMu.Unlock()
	t.Cleanup(func() { linkMu.Lock(); linkMeasured = saved; linkMu.Unlock() })

	var dev api.LinkProbeDevice
	dev.PCIBusID = "00000000:01:00.0"
	dev.Measured.Available, dev.Measured.H2DGBps, dev.Measured.D2HGBps = true, 12.5, 13.1
	at := time.Unix(1_800_000_000, 0)
	rememberLinkProbe(api.LinkProbeResponse{Results: []api.LinkProbeResult{{Backend: "CUDA", Probe: &api.LinkProbe{Devices: []api.LinkProbeDevice{dev}}}}}, at)

	gen := 4
	snap := systel.Snapshot{Devices: []api.SystemDevice{{PCIID: "0000:01:00.0", Link: &api.SystemLink{Gen: &gen}}, {PCIID: "0000:18:00.0"}}}
	resp := (&Server{}).systemResponse(snap, nil)
	l := resp.Devices[0].Link
	if l == nil || *l.Gen != 4 || *l.H2DGBps != 12.5 || *l.D2HGBps != 13.1 || !l.MeasuredAt.Equal(at) {
		t.Errorf("link %+v", l)
	}
	if snap.Devices[0].Link.H2DGBps != nil {
		t.Error("the cached snapshot itself was changed")
	}
	if resp.Devices[1].Link != nil {
		t.Errorf("a GPU never probed has no measurement: %+v", resp.Devices[1].Link)
	}
}

func TestSystemTelemetryIsAdvertised(t *testing.T) {
	if !slices.Contains(xollamaFeatures(), api.FeatureSystemTelemetry) {
		t.Fatal("system_telemetry_v1 is not listed")
	}
}

// With an API key set, the route asks for it like /api/ps does.
func TestTheSystemRouteAsksForTheAPIKey(t *testing.T) {
	keyHome(t)
	t.Setenv("XOLLAMA_API_KEY", testKey)
	w := systemRoute(t, &Server{}, systel.Snapshot{}, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d without the key, want 401", w.Code)
	}
}
