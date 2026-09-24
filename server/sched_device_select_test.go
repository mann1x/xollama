package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/ml"
)

func TestSchedRefusesAModelWhosePinnedDeviceIsAbsent(t *testing.T) {
	ctx, done := context.WithTimeout(t.Context(), 2*time.Second)
	defer done()
	s := InitScheduler(ctx)
	s.waitForRecovery = 10 * time.Millisecond
	s.getGpuFn = getGpuFn // one Metal device
	s.getSystemInfoFn = getSystemInfoFn
	loaded := false
	s.loadFn = func(*LlmRequest, ml.SystemInfo, []ml.DeviceInfo, bool) bool {
		loaded = true
		return false
	}

	a := newScenarioRequest(t, ctx, "pinned-to-renoir", 10, &api.Duration{Duration: 5 * time.Millisecond}, nil)
	a.req.model.Xollama = pinned("Vulkan", "0000:18:00.0")
	s.pendingReqCh <- a.req
	s.Run(ctx)

	select {
	case err := <-a.req.errCh:
		if !strings.Contains(err.Error(), "Vulkan device 0000:18:00.0") {
			t.Fatalf("refusal does not name the pinned device: %v", err)
		}
	case <-a.req.successCh:
		t.Fatal("a pinned model was served without its device")
	case <-ctx.Done():
		t.Fatal("timeout")
	}
	if loaded {
		t.Fatal("the load must not be attempted on other hardware")
	}
}

func TestSchedHandsLoadOnlyThePinnedDevices(t *testing.T) {
	ctx, done := context.WithTimeout(t.Context(), 2*time.Second)
	defer done()
	s := InitScheduler(ctx)
	s.waitForRecovery = 10 * time.Millisecond
	s.getGpuFn = func(context.Context, []ml.FilteredRunnerDiscovery) []ml.DeviceInfo { return solidPCDevices() }
	s.getSystemInfoFn = getSystemInfoFn
	got := make(chan []ml.DeviceInfo, 1)
	s.loadFn = func(req *LlmRequest, _ ml.SystemInfo, gpus []ml.DeviceInfo, _ bool) bool {
		got <- gpus
		req.errCh <- context.Canceled // end the request; only the device list matters here
		return false
	}

	a := newScenarioRequest(t, ctx, "pinned-to-renoir", 10, &api.Duration{Duration: 5 * time.Millisecond}, nil)
	a.req.model.Xollama = pinned("Vulkan", "integrated")
	s.pendingReqCh <- a.req
	s.Run(ctx)

	select {
	case gpus := <-got:
		if len(gpus) != 1 || gpus[0].PCIID != "0000:18:00.0" {
			t.Fatalf("load saw %v, want only the iGPU", ids(gpus))
		}
	case <-ctx.Done():
		t.Fatal("timeout")
	}
}
