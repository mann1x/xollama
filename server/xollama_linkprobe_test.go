package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/llm/engine"
	"github.com/ollama/ollama/ml"
)

const probeJSON = `{"schema":"opencoti.link-probe/1","forced_gbps":null,
 "devices":[{"index":0,"backend":"CUDA","name":"CUDA0","pci_bus_id":"0000:01:00.0",
  "link":{"detected":true,"source":"nvidia-smi","gen":4,"width":16,"geometry_gbps":25.6,"max_gen":4,"max_width":16},
  "measured":{"available":true,"method":"pinned","h2d_gbps":24.9,"d2h_gbps":25.1},"would_use_gbps":24.9}]}`

func probeCall(t *testing.T, s *Server, remote string) (*httptest.ResponseRecorder, api.LinkProbeResponse) {
	t.Helper()
	h, err := s.GenerateRoutes()
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, api.XollamaLinkProbePath, strings.NewReader("{}"))
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var resp api.LinkProbeResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	return w, resp
}

func probeSeams(t *testing.T, gpus []ml.DeviceInfo, run func(context.Context, engine.Backend) ([]byte, error)) {
	t.Helper()
	keyHome(t)
	oldD, oldR := discoverDevices, linkProbeRun
	discoverDevices = func(context.Context, []ml.FilteredRunnerDiscovery) []ml.DeviceInfo { return gpus }
	linkProbeRun = run
	t.Cleanup(func() { discoverDevices, linkProbeRun = oldD, oldR })
}

func TestTheLinkProbeReportsEachBackend(t *testing.T) {
	var probed []engine.Backend
	probeSeams(t, []ml.DeviceInfo{gpu("CUDA", "0", "0000:01:00.0", 24), gpu("Vulkan", "0", "0000:01:00.0", 24), gpu("Metal", "0", "", 8)},
		func(_ context.Context, b engine.Backend) ([]byte, error) {
			probed = append(probed, b)
			if b == engine.BackendVulkan {
				return nil, errors.New("this engine has no link probe")
			}
			return []byte(probeJSON), nil
		})
	w, resp := probeCall(t, &Server{}, "127.0.0.1:4000")
	if w.Code != http.StatusOK || len(resp.Results) != 2 || len(probed) != 2 {
		t.Fatalf("%d %+v (probed %v)", w.Code, resp, probed)
	}
	cuda := resp.Results[0]
	if cuda.Probe == nil || cuda.Probe.Devices[0].Measured.H2DGBps != 24.9 || cuda.Probe.Devices[0].Link.Gen != 4 {
		t.Fatalf("cuda: %+v", cuda)
	}
	if resp.Results[1].Error == "" {
		t.Fatal("a backend whose probe failed reports no error")
	}
}

func TestTheLinkProbeIsLocalOnly(t *testing.T) {
	probeSeams(t, nil, nil)
	if w, _ := probeCall(t, &Server{}, "192.0.2.1:4000"); w.Code != http.StatusForbidden {
		t.Fatalf("remote: %d", w.Code)
	}
}

func TestABackendWithAModelGeneratingIsNotProbed(t *testing.T) {
	ran := false
	probeSeams(t, []ml.DeviceInfo{gpu("CUDA", "0", "0000:01:00.0", 24)}, func(context.Context, engine.Backend) ([]byte, error) {
		ran = true
		return []byte(probeJSON), nil
	})
	s := &Server{sched: &Scheduler{loaded: map[string]*runnerRef{
		"m": {modelPath: "/models/busy", refCount: 1, gpus: []ml.DeviceID{{Library: "CUDA", ID: "0"}}},
	}}}
	_, resp := probeCall(t, s, "127.0.0.1:4000")
	if ran || len(resp.Results) != 1 || !strings.Contains(resp.Results[0].Error, "/models/busy") {
		t.Fatalf("ran=%v %+v", ran, resp)
	}
	// Loaded but idle: the probe runs.
	s.sched.loaded["m"].refCount = 0
	_, resp = probeCall(t, s, "127.0.0.1:4000")
	if !ran || resp.Results[0].Probe == nil {
		t.Fatalf("an idle model blocked the probe: %+v", resp)
	}
}

func TestALinkProbeFailureSaysTheEnginesReason(t *testing.T) {
	for stderr, want := range map[string]string{
		"0.00.1 I llama_server: x\nopencoti --link-probe: measuring\nopencoti --link-probe: no GPU device in the loaded backend family\n": "no GPU device in the loaded backend family",
		"fatal error: support for --gpu nvidia was explicitly requested, but it wasn't available":                                         "support for --gpu nvidia was explicitly requested, but it wasn't available",
		"usage:\n  llamafile -m model.gguf --server":                                                                                      "this engine has no link probe (opencoti b62 or later has it)",
	} {
		if got := linkProbeError(stderr).Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
