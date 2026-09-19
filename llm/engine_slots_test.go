package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/xollama"
	"golang.org/x/sync/semaphore"
)

func cfgWithSlots(sl *xollama.Slots) LlamaServerConfig {
	return LlamaServerConfig{Xollama: &xollama.Config{Version: 1, Slots: sl}}
}

func TestResolveSlotPlan(t *testing.T) {
	tests := []struct {
		name         string
		env          map[string]string
		cfg          LlamaServerConfig
		numParallel  int
		forcedSingle bool
		want         slotPlan
		why          string
	}{
		{
			name:        "on by default, growing from what ollama decided",
			numParallel: 1,
			want:        slotPlan{Dynamic: true, Live: 1, Max: defaultMaxParallel},
			why:         "a default that has to be switched on is a default nobody finds",
		},
		{
			name:        "the environment can switch it off entirely",
			env:         map[string]string{"XOLLAMA_DYNAMIC_SLOTS": "0"},
			numParallel: 2,
			want:        slotPlan{Live: 2},
			why:         "off leaves upstream's fixed split, and nothing else applies",
		},
		{
			name:        "a ceiling can be named",
			env:         map[string]string{"XOLLAMA_MAX_PARALLEL": "8"},
			numParallel: 1,
			want:        slotPlan{Dynamic: true, Live: 1, Max: 8},
		},
		{
			name:        "the model overrides the environment",
			env:         map[string]string{"XOLLAMA_MAX_PARALLEL": "8"},
			cfg:         cfgWithSlots(&xollama.Slots{Max: 2}),
			numParallel: 1,
			want:        slotPlan{Dynamic: true, Live: 1, Max: 2},
		},
		{
			name:        "a model can switch it off for itself",
			cfg:         cfgWithSlots(&xollama.Slots{Dynamic: boolPtr(false)}),
			numParallel: 3,
			want:        slotPlan{Live: 3},
		},
		{
			name:        "both brakes carry through",
			cfg:         cfgWithSlots(&xollama.Slots{Max: 6, TPSFloor: 12.5, VRAMReserveMiB: 1024}),
			numParallel: 1,
			want:        slotPlan{Dynamic: true, Live: 1, Max: 6, TPSFloor: 12.5, VRAMReserveMiB: 1024},
		},
		{
			name:        "a ceiling below the starting point is not a ceiling",
			env:         map[string]string{"XOLLAMA_MAX_PARALLEL": "2"},
			numParallel: 4,
			want:        slotPlan{Dynamic: true, Live: 4, Max: 4},
			why:         "the engine cannot start above its own maximum",
		},
		{
			name:         "an architecture that must stay single-sequence is never grown",
			cfg:          cfgWithSlots(&xollama.Slots{Dynamic: boolPtr(true), Max: 8}),
			env:          map[string]string{"XOLLAMA_MAX_PARALLEL": "8"},
			numParallel:  1,
			forcedSingle: true,
			want:         slotPlan{Live: 1},
			why:          "the deny-list is a correctness decision, not a capacity preference",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			got := resolveSlotPlan(tt.cfg, tt.numParallel, tt.forcedSingle)
			if got != tt.want {
				t.Errorf("plan = %+v, want %+v (%s)", got, tt.want, tt.why)
			}
		})
	}
}

// The arguments are the whole point: a plan that produces none did nothing.
func TestSlotArgs(t *testing.T) {
	plan := slotPlan{Dynamic: true, Live: 1, Max: 4, TPSFloor: 12.5, VRAMReserveMiB: 1024}

	got := appendSlotArgs(nil, plan, 0, true)
	want := []string{
		"--kv-unified", "--max-parallel", "4",
		"--max-parallel-tps-floor", "12.5",
		"--max-parallel-vram-reserve", "1024",
	}
	if !slices.Equal(got, want) {
		t.Errorf("opencoti args = %v, want %v", got, want)
	}

	// --max-parallel is opencoti's own, and --kv-unified changes what -c means,
	// so a stock load must look exactly as it did before.
	if got := appendSlotArgs(nil, plan, 0, false); len(got) != 0 {
		t.Errorf("stock llama.cpp must get no slot flags, got %v", got)
	}
	if got := appendSlotArgs(nil, slotPlan{Live: 2}, 0, true); len(got) != 0 {
		t.Errorf("a plan that is not dynamic must add nothing, got %v", got)
	}
	// Room to grow is what makes the flags worth passing.
	if got := appendSlotArgs(nil, slotPlan{Dynamic: true, Live: 4, Max: 4}, 0, true); len(got) != 0 {
		t.Errorf("a ceiling equal to the starting point must add nothing, got %v", got)
	}
}

// TestSlotArgsWithPools covers the second reason to pass --kv-unified. A pool's
// reserved sequence id is a share of the same cells a parked slot draws on, so
// the flag has to appear for pooling alone -- and exactly once when both
// features want it.
func TestSlotArgsWithPools(t *testing.T) {
	t.Run("pooling alone still needs the shared cache", func(t *testing.T) {
		got := appendSlotArgs(nil, slotPlan{Live: 1}, 2, true)
		want := []string{"--kv-unified", "--polykv-max-pools", "2"}
		if !slices.Equal(got, want) {
			t.Errorf("args = %v, want %v", got, want)
		}
	})

	t.Run("both features, one --kv-unified", func(t *testing.T) {
		got := appendSlotArgs(nil, slotPlan{Dynamic: true, Live: 1, Max: 4}, 2, true)
		want := []string{"--kv-unified", "--max-parallel", "4", "--polykv-max-pools", "2"}
		if !slices.Equal(got, want) {
			t.Errorf("args = %v, want %v", got, want)
		}
		if n := slices.Index(got, "--kv-unified"); n != 0 || slices.Contains(got[1:], "--kv-unified") {
			t.Errorf("--kv-unified must appear exactly once, got %v", got)
		}
	})

	t.Run("stock llama.cpp gets no pool flags", func(t *testing.T) {
		if got := appendSlotArgs(nil, slotPlan{Live: 1}, 2, false); len(got) != 0 {
			t.Errorf("stock llama.cpp must get nothing, got %v", got)
		}
	})
}

func TestResolvePoolCount(t *testing.T) {
	on, off := true, false

	for _, tc := range []struct {
		name string
		env  map[string]string
		cfg  LlamaServerConfig
		want int
	}{
		{
			name: "pooling off by default",
		},
		{
			// The count sizes the feature; it does not switch it on. A server
			// that names a number for every model must not start pooling
			// models that never asked.
			name: "a count alone does not start pooling",
			env:  map[string]string{"XOLLAMA_POLYKV_MAX_POOLS": "4"},
		},
		{
			name: "the environment turns pooling on and gets the default count",
			env:  map[string]string{"XOLLAMA_SESSION_POOL": "1"},
			want: defaultMaxPools,
		},
		{
			name: "the environment sets both",
			env:  map[string]string{"XOLLAMA_SESSION_POOL": "1", "XOLLAMA_POLYKV_MAX_POOLS": "5"},
			want: 5,
		},
		{
			name: "the model turns pooling on",
			cfg:  LlamaServerConfig{Xollama: &xollama.Config{Version: 1, Session: &xollama.Session{Pool: &on}}},
			want: defaultMaxPools,
		},
		{
			name: "the model sizes it",
			cfg:  LlamaServerConfig{Xollama: &xollama.Config{Version: 1, Session: &xollama.Session{Pool: &on, MaxPools: 3}}},
			want: 3,
		},
		{
			name: "the model turns pooling off where the environment turned it on",
			env:  map[string]string{"XOLLAMA_SESSION_POOL": "1", "XOLLAMA_POLYKV_MAX_POOLS": "5"},
			cfg:  LlamaServerConfig{Xollama: &xollama.Config{Version: 1, Session: &xollama.Session{Pool: &off}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := resolvePoolCount(tc.cfg); got != tc.want {
				t.Errorf("resolvePoolCount() = %d, want %d", got, tc.want)
			}
		})
	}
}

// Without this the feature is invisible: ollama would keep feeding one request
// at a time to an engine that had grown to four live slots.
func TestSlotPlanConcurrency(t *testing.T) {
	for _, tt := range []struct {
		name string
		plan slotPlan
		want int
	}{
		{"dynamic raises it to the ceiling", slotPlan{Dynamic: true, Live: 1, Max: 4}, 4},
		{"a fixed split keeps ollama's own number", slotPlan{Live: 3}, 3},
		{"no room to grow changes nothing", slotPlan{Dynamic: true, Live: 4, Max: 4}, 4},
		{"never zero", slotPlan{}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.plan.concurrency(); got != tt.want {
				t.Errorf("concurrency = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRetryAfterHeader(t *testing.T) {
	now := time.Now()
	for _, tt := range []struct {
		name   string
		header string
		want   time.Duration
	}{
		{"seconds", "2", 2 * time.Second},
		{"fractional seconds", "0.5", 500 * time.Millisecond},
		{"an http date", now.Add(3 * time.Second).UTC().Format(http.TimeFormat), 2 * time.Second},
		{"absent", "", admissionRetryFallback},
		{"nonsense", "soon", admissionRetryFallback},
		{"zero", "0", admissionRetryFallback},
		{"a date already past", now.Add(-time.Hour).UTC().Format(http.TimeFormat), admissionRetryFallback},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res := &http.Response{Header: http.Header{}}
			if tt.header != "" {
				res.Header.Set("Retry-After", tt.header)
			}
			got := retryAfter(res, now)
			// HTTP dates have one-second resolution, so allow a second of slack
			// rather than asserting an exact duration.
			if got < tt.want-time.Second || got > tt.want+time.Second {
				t.Errorf("retryAfter(%q) = %v, want about %v", tt.header, got, tt.want)
			}
		})
	}
}

// An engine with dynamic slots refuses what it cannot seat rather than queueing
// it. A caller that never asked for any of this must not start seeing 429s, so
// the wait happens inside.
func TestCompletionWaitsForAdmission(t *testing.T) {
	var refusals atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, `{"status":"ok"}`)
		case "/completion":
			if refusals.Add(1) <= 2 {
				w.Header().Set("Retry-After", "0.01")
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, `{"error":{"code":429,"type":"rate_limit_error"}}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintln(w, `data: {"content":"seated","stop":true}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	parts := strings.Split(srv.URL, ":")
	var port int
	fmt.Sscanf(parts[len(parts)-1], "%d", &port)

	runner := &llamaServerRunner{
		port:         port,
		cmd:          fakeRunningCmd(),
		sem:          semaphore.NewWeighted(1),
		options:      api.Options{Runner: api.Runner{NumCtx: 2048}},
		usedOpencoti: true,
	}

	opts := api.DefaultOptions()
	var got string
	err := runner.Completion(t.Context(), CompletionRequest{Prompt: "hi", Options: &opts},
		func(r CompletionResponse) { got += r.Content })
	if err != nil {
		t.Fatalf("a refused request should have been waited out, not returned: %v", err)
	}
	if got != "seated" {
		t.Errorf("content = %q, want %q", got, "seated")
	}
	if n := refusals.Load(); n != 3 {
		t.Errorf("expected two refusals then a seat, got %d attempts", n)
	}
}

// The wait is the caller's to cancel. A request whose context ends while the
// engine is still refusing must come back promptly, not sit out the budget.
func TestAdmissionWaitHonoursTheCaller(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	runner := &llamaServerRunner{client: newLlamaServerHTTPClient(), usedOpencoti: true}

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	res, err := runner.postWaitingForAdmission(ctx, srv.URL, []byte(`{}`))
	if res != nil {
		res.Body.Close()
	}
	if err == nil {
		t.Fatal("expected the cancelled wait to return an error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("waited %v; the caller's context should have ended it", elapsed)
	}
}

// Only the engine that has an admission gate gets this treatment. Anywhere else
// a 429 is somebody else's answer and has to reach the caller unchanged.
func TestAdmissionWaitIsOpencotiOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	runner := &llamaServerRunner{client: newLlamaServerHTTPClient()}
	res, err := runner.postWaitingForAdmission(t.Context(), srv.URL, []byte(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want the 429 passed through untouched", res.StatusCode)
	}
}
