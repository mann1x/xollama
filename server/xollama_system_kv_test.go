package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/internal/systel"
	"github.com/ollama/ollama/llm"
)

// kvEngine is a runner whose engine has an HTTP surface.
type kvEngine struct {
	*mockLlm
	name  string
	body  string
	calls *atomic.Int32
}

func (k kvEngine) EngineName() string { return k.name }

func (k kvEngine) EngineDo(_ context.Context, call llm.EngineCall) (*http.Response, error) {
	k.calls.Add(1)
	if call.Method != http.MethodGet || call.Endpoint != "kv" {
		return nil, errors.New("asked " + call.Method + " " + call.Endpoint)
	}
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(k.body))}, nil
}

// A 3 GiB window on the GPU and a 1.5 GiB tail in host memory.
const engagedKV = `{"kv_pool_cells":131072,"rolling_kv":{"available":true,"residency_mode":"auto","engaged":true,"kvarn":false,
 "cells":131072,"window_cells":87040,"tail_cells":44032,"window_mib":3072.0,"tail_mib":1536.0}}`

func resetKV(t *testing.T) {
	t.Helper()
	kvMu.Lock()
	saved, savedNow, savedFetch := kvCache, kvNow, kvFetch
	kvCache = map[*runnerRef]*kvEntry{}
	kvMu.Unlock()
	t.Cleanup(func() {
		waitKV(t)
		kvMu.Lock()
		kvCache, kvNow, kvFetch = saved, savedNow, savedFetch
		kvMu.Unlock()
	})
}

// waitKV waits for every background read to land.
func waitKV(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		kvMu.Lock()
		busy := false
		for _, e := range kvCache {
			busy = busy || e.fetching
		}
		kvMu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a KV read never finished")
		}
		time.Sleep(time.Millisecond)
	}
}

func kvRunner(name string, llama llm.LlamaServer) *runnerRef {
	r := &runnerRef{llama: llama}
	r.model.Store(&Model{ShortName: name})
	return r
}

// The request never waits on the engine: the first poll after a load reports
// null and starts the read, the next one has it. A model on stock llama.cpp
// is never asked and reports null.
func TestTheSystemRouteReportsTheEnginesKVWithoutWaitingOnIt(t *testing.T) {
	resetKV(t)
	release := make(chan struct{})
	kvFetch = func(ctx context.Context, reader llm.EngineIntrospector) (*api.SystemModelKV, error) {
		<-release
		return fetchEngineKV(ctx, reader)
	}
	var calls, stockCalls atomic.Int32
	oc := kvRunner("qwen3.6:27b", kvEngine{&mockLlm{}, "opencoti", engagedKV, &calls})
	stock := kvRunner("gemma4:e4b", kvEngine{&mockLlm{}, "llamacpp", engagedKV, &stockCalls})
	s := &Server{sched: &Scheduler{loaded: map[string]*runnerRef{"a": oc, "b": stock}}}

	done := make(chan api.SystemResponse, 1)
	go func() { done <- s.systemResponse(snapNone(), nil) }()
	var first api.SystemResponse
	select {
	case first = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the route waited on the engine's KV read")
	}
	for _, m := range first.Models {
		if m.KV != nil {
			t.Errorf("%s: first poll reported %+v before any reading", m.Name, m.KV)
		}
	}

	close(release)
	waitKV(t)
	second := s.systemResponse(snapNone(), nil)
	byName := map[string]api.SystemModel{}
	for _, m := range second.Models {
		byName[m.Name] = m
	}
	kv := byName["qwen3.6:27b"].KV
	if kv == nil || !kv.Engaged || kv.ResidencyMode != "auto" || kv.Cells != 131072 || kv.WindowCells != 87040 || kv.TailCells != 44032 {
		t.Fatalf("opencoti model KV %+v", kv)
	}
	if kv.DeviceBytes == nil || *kv.DeviceBytes != 3<<30 || kv.HostBytes == nil || *kv.HostBytes != 1536<<20 {
		t.Errorf("bytes device %v host %v", kv.DeviceBytes, kv.HostBytes)
	}
	if kv.AsOf.IsZero() {
		t.Error("a reading carries the time it was taken")
	}
	if byName["gemma4:e4b"].KV != nil || stockCalls.Load() != 0 {
		t.Errorf("stock llama.cpp was asked (%d calls) or reported %+v", stockCalls.Load(), byName["gemma4:e4b"].KV)
	}
}

// Any number of polls cost the engine one read per interval.
func TestTheEngineIsAskedOncePerInterval(t *testing.T) {
	resetKV(t)
	now := time.Unix(1_800_000_000, 0)
	kvNow = func() time.Time { return now }
	var calls atomic.Int32
	r := kvRunner("qwen3.6:27b", kvEngine{&mockLlm{}, "opencoti", engagedKV, &calls})

	for range 5 {
		systemKV(r)
		waitKV(t)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("five polls inside one interval asked the engine %d times, want 1", n)
	}
	now = now.Add(systemInterval)
	systemKV(r)
	waitKV(t)
	if n := calls.Load(); n != 2 {
		t.Fatalf("a poll after the interval did not refresh: %d reads", n)
	}
}

// A read that fails replaces the last reading with null: an old reading is
// never passed off as current.
func TestAFailedReadIsNullNotTheLastReading(t *testing.T) {
	resetKV(t)
	now := time.Unix(1_800_000_000, 0)
	kvNow = func() time.Time { return now }
	var calls atomic.Int32
	r := kvRunner("qwen3.6:27b", kvEngine{&mockLlm{}, "opencoti", engagedKV, &calls})
	systemKV(r)
	waitKV(t)
	if systemKV(r) == nil {
		t.Fatal("no reading after a good read")
	}

	kvFetch = func(context.Context, llm.EngineIntrospector) (*api.SystemModelKV, error) {
		return nil, errors.New("connection refused")
	}
	now = now.Add(systemInterval)
	systemKV(r)
	waitKV(t)
	if kv := systemKV(r); kv != nil {
		t.Errorf("after a failed read the route still reports %+v", kv)
	}
}

// An unloaded runner's reading is dropped with it.
func TestAnUnloadedModelsKVIsForgotten(t *testing.T) {
	resetKV(t)
	var calls atomic.Int32
	r := kvRunner("qwen3.6:27b", kvEngine{&mockLlm{}, "opencoti", engagedKV, &calls})
	s := &Server{sched: &Scheduler{loaded: map[string]*runnerRef{"a": r}}}
	s.systemResponse(snapNone(), nil)
	waitKV(t)
	s.sched.loaded = map[string]*runnerRef{}
	s.systemResponse(snapNone(), nil)
	kvMu.Lock()
	defer kvMu.Unlock()
	if len(kvCache) != 0 {
		t.Errorf("%d entries left after the unload", len(kvCache))
	}
}

// The engine writes a size of 0 as null and every size as null under KVarN:
// host 0 comes from the counts, and null only means a tail whose size the
// engine does not give.
func TestTheKVReportKeepsNullApartFromZero(t *testing.T) {
	mib := func(v float64) *float64 { return &v }
	for _, tt := range []struct {
		name       string
		in         engineRollingKV
		wantNil    bool
		host, dev  *uint64
		wantKVarN  bool
		wantEngage bool
	}{
		{name: "no report", in: engineRollingKV{Available: false}, wantNil: true},
		{name: "window not engaged", in: engineRollingKV{Available: true, ResidencyMode: "auto", Cells: 32768, WindowCells: 32768}, host: u64(0)},
		{name: "engaged", in: engineRollingKV{Available: true, Engaged: true, Cells: 4096, WindowCells: 3072, TailCells: 1024, WindowMiB: mib(96), TailMiB: mib(32)}, host: u64(32 << 20), dev: u64(96 << 20), wantEngage: true},
		{name: "KVarN tail without sizes", in: engineRollingKV{Available: true, Engaged: true, KVarN: true, Cells: 4096, WindowCells: 3072, TailCells: 1024}, wantKVarN: true, wantEngage: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			kv := kvFromEngine(tt.in)
			if tt.wantNil {
				if kv != nil {
					t.Fatalf("got %+v, want null", kv)
				}
				return
			}
			if kv == nil || kv.KVarN != tt.wantKVarN || kv.Engaged != tt.wantEngage {
				t.Fatalf("got %+v", kv)
			}
			if !eqPtr(kv.HostBytes, tt.host) || !eqPtr(kv.DeviceBytes, tt.dev) {
				t.Errorf("host %v device %v, want %v %v", ptrStr(kv.HostBytes), ptrStr(kv.DeviceBytes), ptrStr(tt.host), ptrStr(tt.dev))
			}
		})
	}
}

func TestKVHostTelemetryIsAdvertised(t *testing.T) {
	if !slices.Contains(xollamaFeatures(), api.FeatureKVHostTelemetry) {
		t.Fatal("kv_host_telemetry_v1 is not listed")
	}
}

func eqPtr(a, b *uint64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func ptrStr(p *uint64) string {
	if p == nil {
		return "null"
	}
	return strconv.FormatUint(*p, 10)
}

func snapNone() systel.Snapshot { return systel.Snapshot{} }
