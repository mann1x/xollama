package llm

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/ollama/ollama/api"
)

// A native chat placed on a client's pool teaches the automatic pools
// nothing -- its prefix is the client's pool, not ours -- while an unplaced
// one with the same key is still learned from.
func TestAPlacedChatFeedsNoAutomaticPool(t *testing.T) {
	var learn atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, `{"status":"ok"}`)
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`)
			fmt.Fprintln(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`)
			fmt.Fprintln(w, `data: [DONE]`)
		default:
			// apply-template, tokenize: what capture asks, and only capture.
			learn.Add(1)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	parts := strings.Split(srv.URL, ":")
	var port int
	fmt.Sscanf(parts[len(parts)-1], "%d", &port)

	chat := func(p *Placement) {
		runner := &llamaServerRunner{
			port: port, cmd: fakeRunningCmd(), sem: semaphore.NewWeighted(1),
			options: api.Options{Runner: api.Runner{NumCtx: 2048}}, usedOpencoti: true, pools: newPoolRegistry(2),
		}
		opts := api.DefaultOptions()
		if err := runner.Chat(t.Context(), ChatRequest{
			Messages: []api.Message{{Role: "user", Content: "hi"}}, Options: &opts, PoolKey: "k", Placement: p,
		}, func(ChatResponse) {}); err != nil {
			t.Fatal(err)
		}
	}
	pool := 0
	chat(&Placement{PoolID: &pool})
	time.Sleep(200 * time.Millisecond)
	if n := learn.Load(); n != 0 {
		t.Fatalf("a placed chat was learned from: %d capture calls", n)
	}
	chat(nil)
	deadline := time.Now().Add(2 * time.Second)
	for learn.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if learn.Load() == 0 {
		t.Fatal("the unplaced chat was not learned from either: the test proves nothing")
	}
}
