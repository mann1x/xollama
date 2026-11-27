package llm

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

// Two identical cards that the engine numbers the other way round from
// discovery (lines in the r19 form, opencoti mail #2403: pci= after the free
// memory). By name and description the two sides agree; only the address
// tells the cards apart.
const swappedCardsLog = `llama_prepare_model_devices: using device CUDA0 (NVIDIA GeForce RTX 3090) (0000:02:00.0) - 23000 MiB free pci=0000:02:00.0
llama_prepare_model_devices: using device CUDA1 (NVIDIA GeForce RTX 3090) (0000:01:00.0) - 12000 MiB free pci=0000:01:00.0
load_tensors:        CUDA0 model buffer size =  1000.00 MiB
load_tensors:        CUDA1 model buffer size =  2000.00 MiB
`

func TestTheEnginesDeviceIsFoundByItsPCIAddress(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "")
	a := ml.DeviceInfo{DeviceID: ml.DeviceID{ID: "0", Library: "CUDA"}, Name: "CUDA0", Description: "NVIDIA GeForce RTX 3090", PCIID: "0000:01:00.0"}
	b := ml.DeviceInfo{DeviceID: ml.DeviceID{ID: "1", Library: "CUDA"}, Name: "CUDA1", Description: "NVIDIA GeForce RTX 3090", PCIID: "0000:02:00.0"}
	r := &llamaServerRunner{usedOpencoti: true, vramByDevice: map[string]uint64{}, systemFreeAtLoad: map[string]uint64{}, gpus: []ml.DeviceInfo{a, b}}
	w := &memoryParsingWriter{inner: io.Discard, runner: r}
	if _, err := w.Write([]byte(swappedCardsLog)); err != nil {
		t.Fatal(err)
	}
	if got := r.engineKey(a); got != "CUDA1" {
		t.Fatalf("discovery's CUDA0 (0000:01:00.0) is the engine's %s, want CUDA1, the device with its address", got)
	}
	if got := r.VRAMByGPU(b.DeviceID) >> 20; got != 1000 {
		t.Fatalf("discovery's CUDA1 holds %d MiB, want the engine's CUDA0 buffers (1000)", got)
	}
	// Stock llama.cpp keeps upstream's lookup.
	r.usedOpencoti = false
	if got := r.engineKey(a); got != "CUDA0" {
		t.Fatalf("stock llama.cpp looked %s up as %s", a.Name, got)
	}
}

// The r18 line has no address; the order-based match still answers.
func TestADeviceLineWithoutAnAddressIsStillRead(t *testing.T) {
	r := renumberedRunner(t, true, rx9070)
	if got := r.engineKey(rx9070); got != "Vulkan0" {
		t.Fatalf("engine name %s, want Vulkan0", got)
	}
	if len(r.enginePCI) != 0 {
		t.Fatalf("an address was read from a line that has none: %v", r.enginePCI)
	}
}

func TestTheMoECacheReachesTheEngineAsStated(t *testing.T) {
	share := 0.25
	off := false
	for _, tc := range []struct {
		name     string
		fit      *xollama.Fit
		wantArgs []string
		wantEnv  map[string]string
	}{
		{"nothing stated", nil, nil, nil},
		{"a size", &xollama.Fit{MoECache: "4096"}, []string{"--moe-cache-mib", "4096"}, nil},
		{"a share", &xollama.Fit{MoECache: "auto", MoECacheCPUShare: &share}, []string{"--moe-cache-cpu-share", "0.25"}, map[string]string{envMoECacheAuto: "1"}},
		{"off", &xollama.Fit{MoECache: "off"}, nil, map[string]string{envMoECacheAuto: "0"}},
		{"no warm start", &xollama.Fit{MoECacheWarmStart: &off}, nil, map[string]string{envMoECacheProfile: "0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := &xollama.Config{Version: 1, Fit: tc.fit}
			args := appendEnginePolicyArgs(nil, x, true)
			if !slices.Equal(args, tc.wantArgs) {
				t.Fatalf("args %q, want %q", args, tc.wantArgs)
			}
			in := map[string]string{"HOME": "/h"}
			env := withMoECacheEnvs(in, x)
			for k, v := range tc.wantEnv {
				if env[k] != v {
					t.Fatalf("%s=%q, want %q (env %v)", k, env[k], v, env)
				}
			}
			if len(tc.wantEnv) == 0 && len(env) != 1 {
				t.Fatalf("env %v, want it unchanged", env)
			}
			if len(in) != 1 {
				t.Fatalf("the caller's map was changed: %v", in)
			}
			// Stock llama.cpp gets none of it.
			if args := appendEnginePolicyArgs(nil, x, false); len(args) != 0 {
				t.Fatalf("stock llama.cpp got %q", args)
			}
		})
	}
}

func TestTheMoECacheIsRefusedOnAnEngineWithoutIt(t *testing.T) {
	old := policyUnavailable
	t.Cleanup(func() { policyUnavailable = old })
	policyUnavailable = func(setting, feature string) string {
		if feature == "moe_cache_v1" {
			return setting + " needs " + feature
		}
		return ""
	}
	x := &xollama.Config{Version: 1, Fit: &xollama.Fit{MoECache: "off"}}
	if err := enginePolicyRefusal(x, true); err == nil || !strings.Contains(err.Error(), "fit.moe_cache") {
		t.Fatalf("refusal %v, want fit.moe_cache named", err)
	}
	if err := enginePolicyRefusal(x, false); err != nil {
		t.Fatalf("stock llama.cpp refused: %v", err)
	}
}

// The /props body of the r19 release engine on solidPC's 3090 (2026-11-27,
// Qwen3.6 27B A3B under a 9000 MiB VRAM target, --spec-type none).
const moeCacheProps = `{"features":["moe_cache_v1","moe_cache_auto_v1","moe_cache_state_v1","device_pci_v1"],
"opencoti":{"devices":[{"name":"CUDA0","description":"NVIDIA GeForce RTX 3090","id":"0000:01:00.0","pci":"0000:01:00.0",
"moe_cache":{"verdict":"pays","reason":null,"inputs":{"upload_ms":0.310807125,"upload_first_ms":0.313448125,"warm_up_ms":202.167363,
"link_forced":false,"link_idle":false,"cpu_ms":0.064222375,"cpu_threads":6,"split_ms":0.4,"miss":0.03,"ram_ms_per_token":35.4356075,
"cached_ms_per_token":20.2229276375},"layers_ram_fit":34,"layers_ram_final":41,"cache_mib":2452,"host_expert_mib":13369,"admission":true,
"mmap_pageable":false,"profile":{"path":"/h/.llamafile/moe-cache/f335783fc163c1be.mcp","read":false,"warm_experts":0}}}]}}`

func TestTheMoECacheRecordIsReadFromProps(t *testing.T) {
	got := parseMoECacheState([]byte(moeCacheProps))
	if len(got) != 1 {
		t.Fatalf("%d devices, want 1", len(got))
	}
	d := got[0]
	if d.Device != "CUDA0" || d.PCI != "0000:01:00.0" || d.Verdict != "pays" || *d.CacheMiB != 2452 || *d.LayersRAMFinal != 41 ||
		d.Profile == nil || !strings.HasSuffix(d.Profile.Path, "f335783fc163c1be.mcp") || d.Reason != nil {
		t.Fatalf("record %+v", d)
	}
	// Without the feature row the engine keeps no such record.
	if parseMoECacheState([]byte(strings.Replace(moeCacheProps, `"moe_cache_state_v1",`, "", 1))) != nil {
		t.Fatal("a record was read from an engine that does not declare moe_cache_state_v1")
	}
}

// An opencoti engine is asked to shut down before it is killed: a clean exit
// is what writes the MoE cache's warm-start profile.
func TestAnOpencotiEngineIsAskedToShutDownBeforeTheKill(t *testing.T) {
	old := hasGracefulShutdown
	t.Cleanup(func() { hasGracefulShutdown = old })
	hasGracefulShutdown = func() bool { return true }

	done := make(chan struct{})
	var asked atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/shutdown" {
			asked.Add(1)
			w.WriteHeader(http.StatusOK)
			go func() { time.Sleep(20 * time.Millisecond); close(done) }()
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.Listener = l
	srv.Start()
	defer srv.Close()
	port := l.Addr().(*net.TCPAddr).Port

	r := &llamaServerRunner{usedOpencoti: true, port: port, done: done}
	if !r.stopGracefully() || asked.Load() != 1 {
		t.Fatalf("graceful stop not taken (asked %d times)", asked.Load())
	}

	// Stock llama.cpp is killed at once, as upstream does.
	stock := &llamaServerRunner{usedOpencoti: false, port: port, done: make(chan struct{})}
	if stock.stopGracefully() || asked.Load() != 1 {
		t.Fatal("stock llama.cpp was asked to shut down")
	}
	// An engine without the route is killed at once.
	hasGracefulShutdown = func() bool { return false }
	if (&llamaServerRunner{usedOpencoti: true, port: port, done: make(chan struct{})}).stopGracefully() {
		t.Fatal("an engine without graceful_shutdown_v1 was asked")
	}
}

// An engine that takes the request and does not exit is killed after the
// wait.
func TestAnEngineThatDoesNotExitIsKilledAfterTheWait(t *testing.T) {
	oldF, oldW := hasGracefulShutdown, gracefulStopWait
	t.Cleanup(func() { hasGracefulShutdown, gracefulStopWait = oldF, oldW })
	hasGracefulShutdown = func() bool { return true }
	gracefulStopWait = 50 * time.Millisecond
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	srv.Listener = l
	srv.Start()
	defer srv.Close()
	r := &llamaServerRunner{usedOpencoti: true, port: l.Addr().(*net.TCPAddr).Port, done: make(chan struct{})}
	if r.stopGracefully() {
		t.Fatal("reported an exit that did not happen")
	}
}
