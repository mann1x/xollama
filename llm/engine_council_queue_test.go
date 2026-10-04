package llm

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// refusingEngine answers every request with the engine's 429 until it has
// refused `refusals` times, then 200.
func refusingEngine(t *testing.T, refusals int32) (*llamaServerRunner, *atomic.Int32) {
	t.Helper()
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if asked.Add(1) <= refusals {
			w.Header().Set("Retry-After", "1") // the engine speaks whole seconds
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"message":"session allocation full (12 of 4096 cells free, needs 900)"}}`))
			return
		}
		w.Write([]byte(`{"id":0}`))
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	return &llamaServerRunner{port: port, client: srv.Client(), usedOpencoti: true, cmd: &exec.Cmd{}}, &asked
}

// TestAQueuedEngineRequestWaitsOutA429: a refusal the engine lifts is waited
// out, as before.
func TestAQueuedEngineRequestWaitsOutA429(t *testing.T) {
	s, asked := refusingEngine(t, 1)
	status, _, err := s.engineRequestQueued(t.Context(), http.MethodPost, "/polykv/pools", nil)
	if err != nil || status != http.StatusOK || asked.Load() != 2 {
		t.Fatalf("status %d err %v after %d requests, want 200 on the second", status, err, asked.Load())
	}
}

// TestAQueuedEngineRequestGivesUpNamingTheRefusal: an engine that never lifts
// its 429 is no longer waited on until the client gives up; past the budget
// the request fails, naming the engine's last refusal.
func TestAQueuedEngineRequestGivesUpNamingTheRefusal(t *testing.T) {
	was := engineQueueBudget
	engineQueueBudget = 1500 * time.Millisecond // room for one 1 s wait, not two
	t.Cleanup(func() { engineQueueBudget = was })
	s, asked := refusingEngine(t, 1<<30)

	start := time.Now()
	_, _, err := s.engineRequestQueued(t.Context(), http.MethodPost, "/polykv/pools", nil)
	if !errors.Is(err, errNoAdmission) {
		t.Fatalf("err %v, want errNoAdmission once the budget is spent", err)
	}
	if !strings.Contains(err.Error(), "session allocation full") || !strings.Contains(err.Error(), "/polykv/pools") {
		t.Errorf("the error must name the request and the engine's last refusal, got %q", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("gave up after %v, want about the 1.5 s budget", took)
	}
	if asked.Load() != 2 {
		t.Errorf("asked %d times, want one refusal waited out and the second given up on", asked.Load())
	}
}
