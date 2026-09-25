// Package probe checks the library behaviours NOTES.md reports, against the
// library alone (no council code). Each test logs what it measured; none
// asserts a library bug, so the probe keeps passing if upstream fixes one.
package probe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/smallnest/langgraphgo/graph"
)

type tok struct {
	Req  int
	Text string
}

// Native token streaming: a node pushes EventToken through its own
// ListenableNode, the only hook a node has into the library's stream.
func streamingGraph(tokens int, cfg graph.StreamConfig) *graph.StreamingRunnable[tok] {
	g := graph.NewStreamingStateGraphWithConfig[tok](cfg)
	var ln *graph.ListenableNode[tok]
	ln = g.AddNode("member", "", func(ctx context.Context, s tok) (tok, error) {
		for i := range tokens {
			ln.NotifyListeners(ctx, graph.EventToken, tok{Req: s.Req, Text: fmt.Sprint(i)}, nil)
		}
		return s, nil
	})
	g.AddEdge("member", graph.END)
	g.SetEntryPoint("member")
	r, err := g.CompileStreaming()
	if err != nil {
		panic(err)
	}
	return r
}

func TestNativeStreamSlowConsumer(t *testing.T) {
	const n = 5000
	for _, mode := range []graph.StreamMode{graph.StreamModeDebug, graph.StreamModeMessages} {
		cfg := graph.DefaultStreamConfig()
		cfg.Mode = mode
		r := streamingGraph(n, cfg)
		t0 := time.Now()
		res := r.Stream(t.Context(), tok{})
		got := 0
		for e := range res.Events {
			if e.Event == graph.EventToken {
				got++
				time.Sleep(20 * time.Microsecond)
			}
		}
		t.Logf("mode %-8s: %d of %d token events delivered (%d dropped), %v", mode, got, n, n-got, time.Since(t0))
	}
}

func TestNativeStreamCrossTalk(t *testing.T) {
	r := streamingGraph(50, graph.DefaultStreamConfig())
	var wg sync.WaitGroup
	foreign := make([]int, 2)
	for req := range 2 {
		wg.Go(func() {
			res := r.Stream(t.Context(), tok{Req: req + 1})
			for e := range res.Events {
				if e.Event == graph.EventToken && e.State.Req != req+1 {
					foreign[req]++
				}
			}
		})
	}
	wg.Wait()
	t.Logf("two concurrent Streams on one compiled runnable: foreign events seen %v", foreign)
}

func TestStreamCloseLatency(t *testing.T) {
	r := streamingGraph(1, graph.DefaultStreamConfig())
	t0 := time.Now()
	res := r.Stream(t.Context(), tok{})
	for range res.Events {
	}
	t.Logf("Stream of a one-node graph took %v end to end", time.Since(t0))
}

type st struct{ N int }

func TestSiblingNotCancelled(t *testing.T) {
	g := graph.NewStateGraph[st]()
	g.AddNode("start", "", func(_ context.Context, s st) (st, error) { return s, nil })
	boom := errors.New("boom")
	var sawCancel atomic.Bool
	g.AddNode("fail", "", func(context.Context, st) (st, error) { return st{}, boom })
	g.AddNode("slow", "", func(ctx context.Context, s st) (st, error) {
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			sawCancel.Store(true)
			return s, ctx.Err()
		}
		return s, nil
	})
	g.AddEdge("start", "fail")
	g.AddEdge("start", "slow")
	g.AddEdge("fail", graph.END)
	g.AddEdge("slow", graph.END)
	g.SetEntryPoint("start")
	r, _ := g.Compile()
	t0 := time.Now()
	_, err := r.Invoke(t.Context(), st{})
	t.Logf("a failing node's sibling: cancelled=%v, Invoke returned after %v with %v", sawCancel.Load(), time.Since(t0), err)
}

func TestReturnedErrorIsLowestIndex(t *testing.T) {
	// With our own cancel-on-failure, which error does Invoke return?
	boom := errors.New("boom")
	counts := map[string]int{}
	for range 200 {
		ctx, cancel := context.WithCancel(t.Context())
		g := graph.NewStateGraph[st]()
		g.AddNode("start", "", func(_ context.Context, s st) (st, error) { return s, nil })
		g.AddNode("fail", "", func(context.Context, st) (st, error) { cancel(); return st{}, boom })
		g.AddNode("slow", "", func(ctx context.Context, s st) (st, error) { <-ctx.Done(); return s, ctx.Err() })
		g.AddEdge("start", "fail")
		g.AddEdge("start", "slow")
		g.AddEdge("fail", graph.END)
		g.AddEdge("slow", graph.END)
		g.SetEntryPoint("start")
		r, _ := g.Compile()
		_, err := r.Invoke(ctx, st{})
		switch {
		case errors.Is(err, boom):
			counts["failure"]++
		case errors.Is(err, context.Canceled):
			counts["sibling's context.Canceled"]++
		}
		cancel()
	}
	t.Logf("error Invoke returned over 200 runs: %v", counts)
}

func TestNoCtxCheckBetweenSupersteps(t *testing.T) {
	g := graph.NewStateGraph[st]()
	for i := range 5 {
		g.AddNode(fmt.Sprint(i), "", func(_ context.Context, s st) (st, error) { s.N++; return s, nil })
		if i > 0 {
			g.AddEdge(fmt.Sprint(i-1), fmt.Sprint(i))
		}
	}
	g.AddEdge("4", graph.END)
	g.SetEntryPoint("0")
	r, _ := g.Compile()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s, err := r.Invoke(ctx, st{})
	t.Logf("Invoke on an already-cancelled ctx: %d of 5 ctx-ignoring nodes ran, err %v", s.N, err)
}

func TestFanOutOrder(t *testing.T) {
	g := graph.NewStateGraph[[]string]()
	g.AddNode("start", "", func(_ context.Context, s []string) ([]string, error) { return nil, nil })
	for i := range 4 {
		name := fmt.Sprint("w", i)
		g.AddNode(name, "", func(context.Context, []string) ([]string, error) { return []string{name}, nil })
		g.AddEdge("start", name)
		g.AddEdge(name, "join")
	}
	g.AddNode("join", "", func(_ context.Context, s []string) ([]string, error) { return s, nil })
	g.AddEdge("join", graph.END)
	g.SetEntryPoint("start")
	g.SetStateMerger(func(_ context.Context, cur []string, news [][]string) ([]string, error) {
		var out []string
		for _, n := range news {
			out = append(out, n...)
		}
		if len(news) == 1 {
			return news[0], nil
		}
		return out, nil
	})
	r, _ := g.Compile()
	orders := map[string]bool{}
	for range 200 {
		s, _ := r.Invoke(t.Context(), nil)
		orders[strings.Join(s, ",")] = true
	}
	t.Logf("distinct orders the merger saw for 4 static fan-out branches over 200 runs: %d", len(orders))
}

// Build+Compile cost per shape, and the per-Invoke cost of an empty node.
func BenchmarkBuildCompile(b *testing.B) {
	for _, w := range []int{2, 8} {
		b.Run(fmt.Sprint("width=", w), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				g := graph.NewStateGraph[st]()
				g.AddNode("plan", "", func(_ context.Context, s st) (st, error) { return s, nil })
				for i := range w {
					g.AddNode(fmt.Sprint("r", i), "", func(_ context.Context, s st) (st, error) { return s, nil })
					g.AddEdge("plan", fmt.Sprint("r", i))
					for j := range w {
						g.AddEdge(fmt.Sprint("r", i), fmt.Sprint("c", j))
					}
				}
				for j := range w {
					g.AddNode(fmt.Sprint("c", j), "", func(_ context.Context, s st) (st, error) { return s, nil })
					g.AddEdge(fmt.Sprint("c", j), graph.END)
				}
				g.SetEntryPoint("plan")
				if _, err := g.Compile(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkInvokeOneNode(b *testing.B) {
	g := graph.NewStateGraph[st]()
	g.AddNode("n", "", func(_ context.Context, s st) (st, error) { return s, nil })
	g.AddEdge("n", graph.END)
	g.SetEntryPoint("n")
	r, _ := g.Compile()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := r.Invoke(b.Context(), st{}); err != nil {
			b.Fatal(err)
		}
	}
}
