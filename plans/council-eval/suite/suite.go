// Package suite is the one test and benchmark suite every candidate runs, so
// the numbers compare like with like. Each impl's _test.go calls Tests and
// Bench with its own runner.
package suite

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"councileval/council"
	"councileval/stub"
)

// Conv builds a conversation whose document is size bytes, ending in q.
func Conv(q string, size int) []council.Message {
	doc := strings.Repeat("The scheduler pins a model to a device. ", size/40+1)[:size]
	return []council.Message{
		{Role: "system", Content: council.Charter},
		{Role: "user", Content: "Here is a design document:\n\n" + doc},
		{Role: "assistant", Content: "I have read the document. What would you like to know?"},
		{Role: "user", Content: q},
	}
}

const hard = "Review the design: what are its three weakest points and how would you fix each?"

type recorder struct {
	mu     sync.Mutex
	events []council.Event
	delay  time.Duration
	first  map[council.Kind]time.Time
}

func (r *recorder) emit(e council.Event) {
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.first == nil {
		r.first = map[council.Kind]time.Time{}
	}
	if _, ok := r.first[e.Kind]; !ok {
		r.first[e.Kind] = time.Now()
	}
	r.events = append(r.events, e)
}

func (r *recorder) text(k council.Kind, role council.Role, i, round int) string {
	var b strings.Builder
	for _, e := range r.events {
		if e.Kind == k && e.Role == role && e.Index == i && e.Round == round {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

// expect is what the stub streams for a member.
func expect(m *stub.Model, role council.Role, i, round int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<%s%d.%d>", role, i+1, round)
	for t := range max(m.Tokens, 1) {
		b.WriteString(strings.Repeat(string(rune('a'+t%26)), max(m.TokenBytes, 1)))
	}
	if role == council.Critic && round < m.Revise {
		b.WriteString(" " + council.Revise)
	}
	return b.String()
}

func count(calls []council.Request, role council.Role) (n int) {
	for _, c := range calls {
		if c.Role == role && !c.RouteOnly {
			n++
		}
	}
	return n
}

// Tests is the behaviour every runner must have.
func Tests(t *testing.T, newRunner func() council.Runner) {
	t.Run("direct", func(t *testing.T) {
		m := &stub.Model{}
		rec := &recorder{}
		res, err := newRunner().Run(t.Context(), council.Defaults(), m, Conv("Hello!", 1024), rec.emit)
		if err != nil {
			t.Fatal(err)
		}
		if res.Route != "direct" || len(m.Calls()) != 2 {
			t.Fatalf("route %q with %d calls, want direct with 2 (decision + answer)", res.Route, len(m.Calls()))
		}
		for _, e := range rec.events {
			if e.Kind != council.Content || e.Role != council.Planner {
				t.Fatalf("direct path emitted %+v; want planner content only", e)
			}
		}
		if got := rec.text(council.Content, council.Planner, 0, 0); got != res.Answer || got == "" {
			t.Fatalf("streamed %q, answered %q", got, res.Answer)
		}
	})

	t.Run("council", func(t *testing.T) {
		m := &stub.Model{SlowFirst: 5 * time.Millisecond}
		rec := &recorder{}
		cfg := council.Defaults()
		res, err := newRunner().Run(t.Context(), cfg, m, Conv(hard, 1024), rec.emit)
		if err != nil {
			t.Fatal(err)
		}
		calls := m.Calls()
		if res.Route != "council" || len(calls) != 3+cfg.Researchers+cfg.Critics {
			t.Fatalf("route %q, %d calls; want council, %d", res.Route, len(calls), 3+cfg.Researchers+cfg.Critics)
		}
		if count(calls, council.Researcher) != 2 || count(calls, council.Critic) != 2 || count(calls, council.Synthesizer) != 1 {
			t.Fatalf("calls by role wrong: %d researchers %d critics %d synth", count(calls, council.Researcher), count(calls, council.Critic), count(calls, council.Synthesizer))
		}
		for _, e := range rec.events {
			if e.Kind == council.Content && e.Role != council.Synthesizer {
				t.Fatalf("content from %s%d: only the synthesizer answers", e.Role, e.Index)
			}
		}
		if got := rec.text(council.Content, council.Synthesizer, 0, 0); got != res.Answer {
			t.Fatalf("streamed answer %q != result %q", got, res.Answer)
		}
		for i := range 2 {
			if got, want := rec.text(council.Thinking, council.Researcher, i, 0), expect(m, council.Researcher, i, 0); got != want {
				t.Fatalf("researcher %d streamed %q, want %q", i, got, want)
			}
			if got, want := rec.text(council.Thinking, council.Critic, i, 0), expect(m, council.Critic, i, 0); got != want {
				t.Fatalf("critic %d streamed %q, want %q", i, got, want)
			}
		}
	})

	t.Run("findings in researcher order", func(t *testing.T) {
		m := &stub.Model{SlowFirst: 10 * time.Millisecond}
		if _, err := newRunner().Run(t.Context(), council.Defaults(), m, Conv(hard, 256), func(council.Event) {}); err != nil {
			t.Fatal(err)
		}
		for _, c := range m.Calls() {
			last := c.Messages[len(c.Messages)-1].Content
			switch c.Role {
			case council.Critic:
				f := c.Messages[len(c.Messages)-2].Content
				want := "FINDINGS OF RESEARCHER 1:\n" + expect(m, council.Researcher, 0, 0) + "\n\nFINDINGS OF RESEARCHER 2:\n" + expect(m, council.Researcher, 1, 0)
				if f != want {
					t.Fatalf("critic saw findings\n%q\nwant\n%q", f, want)
				}
			case council.Synthesizer:
				cr := c.Messages[len(c.Messages)-2].Content
				want := "CRITIQUE 1:\n" + expect(m, council.Critic, 0, 0) + "\n\nCRITIQUE 2:\n" + expect(m, council.Critic, 1, 0)
				if cr != want || !strings.HasPrefix(last, "ROLE: SYNTHESIZER") {
					t.Fatalf("synthesizer saw critiques\n%q\nwant\n%q", cr, want)
				}
			}
		}
	})

	t.Run("hide deliberation", func(t *testing.T) {
		m := &stub.Model{}
		rec := &recorder{}
		cfg := council.Defaults()
		cfg.ShowDeliberation = false
		res, err := newRunner().Run(t.Context(), cfg, m, Conv(hard, 256), rec.emit)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range rec.events {
			if e.Kind == council.Thinking {
				t.Fatalf("thinking event %+v with deliberation hidden", e)
			}
		}
		if res.Answer != expect(m, council.Synthesizer, 0, 0) {
			t.Fatalf("answer changed with deliberation hidden: %q", res.Answer)
		}
	})

	t.Run("seeds and jitter", func(t *testing.T) {
		cfg := council.Defaults()
		cfg.Researchers, cfg.Critics = 4, 4
		m := &stub.Model{}
		if _, err := newRunner().Run(t.Context(), cfg, m, Conv(hard, 256), func(council.Event) {}); err != nil {
			t.Fatal(err)
		}
		seen := map[int64]bool{}
		jittered := 0
		lo, hi := cfg.Temperature*(1-cfg.Jitter), cfg.Temperature*(1+cfg.Jitter)
		for _, c := range m.Calls() {
			if seen[c.Seed] {
				t.Fatalf("seed %d reused (%s%d)", c.Seed, c.Role, c.Index)
			}
			seen[c.Seed] = true
			switch c.Role {
			case council.Researcher, council.Critic:
				if c.Temperature < lo || c.Temperature > hi {
					t.Fatalf("%s%d temperature %v outside [%v,%v]", c.Role, c.Index, c.Temperature, lo, hi)
				}
				if c.Temperature != cfg.Temperature {
					jittered++
				}
			default:
				if c.Temperature != cfg.Temperature {
					t.Fatalf("%s temperature %v, want the model's %v", c.Role, c.Temperature, cfg.Temperature)
				}
			}
		}
		if jittered == 0 {
			t.Fatal("no researcher or critic temperature was jittered")
		}

		base := int64(42)
		cfg.Seed = &base
		key := func(m *stub.Model) []string {
			var k []string
			for _, c := range m.Calls() {
				k = append(k, fmt.Sprintf("%s/%d/%v/%d/%v", c.Role, c.Index, c.RouteOnly, c.Seed, c.Temperature))
			}
			slices.Sort(k)
			return k
		}
		a, b := &stub.Model{}, &stub.Model{}
		for _, mm := range []*stub.Model{a, b} {
			if _, err := newRunner().Run(t.Context(), cfg, mm, Conv(hard, 256), func(council.Event) {}); err != nil {
				t.Fatal(err)
			}
		}
		if !slices.Equal(key(a), key(b)) {
			t.Fatal("a fixed seed did not reproduce the run's seeds and temperatures")
		}
	})

	for _, w := range []int{3, 8} {
		t.Run(fmt.Sprintf("parallel width %d", w), func(t *testing.T) {
			cfg := council.Defaults()
			cfg.Researchers, cfg.Critics = w, w
			m := &stub.Model{Tokens: 10, PerToken: 2 * time.Millisecond}
			if _, err := newRunner().Run(t.Context(), cfg, m, Conv(hard, 256), func(council.Event) {}); err != nil {
				t.Fatal(err)
			}
			for _, r := range []council.Role{council.Researcher, council.Critic} {
				if m.PeakOf(r) != w {
					t.Fatalf("%s peak concurrency %d, want %d: they did not run in parallel", r, m.PeakOf(r), w)
				}
			}
			if m.Peak() != int64(w) {
				t.Fatalf("peak concurrency %d, want %d: phases overlapped", m.Peak(), w)
			}
		})
	}

	t.Run("bounded loop", func(t *testing.T) {
		cfg := council.Defaults()
		cfg.MaxRounds = 2
		m := &stub.Model{Revise: 5} // critics always ask; the bound must stop it
		res, err := newRunner().Run(t.Context(), cfg, m, Conv(hard, 256), func(council.Event) {})
		if err != nil {
			t.Fatal(err)
		}
		if res.Rounds != 2 || count(m.Calls(), council.Researcher) != 4 || count(m.Calls(), council.Critic) != 4 {
			t.Fatalf("rounds %d, %d researcher and %d critic calls; want 2, 4, 4", res.Rounds, count(m.Calls(), council.Researcher), count(m.Calls(), council.Critic))
		}
		for _, c := range m.Calls() {
			if c.Role == council.Researcher && c.Round == 1 && !strings.Contains(c.Messages[len(c.Messages)-2].Content, "CRITIQUE 1:") {
				t.Fatal("second-round researcher did not see the critiques")
			}
		}
		m = &stub.Model{Revise: 5}
		cfg.MaxRounds = 1
		if res, _ = newRunner().Run(t.Context(), cfg, m, Conv(hard, 256), func(council.Event) {}); res.Rounds != 1 {
			t.Fatalf("max_rounds 1 ran %d rounds", res.Rounds)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
		m := &stub.Model{Tokens: 1000, PerToken: 5 * time.Millisecond}
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(50*time.Millisecond, cancel)
		t0 := time.Now()
		_, err := newRunner().Run(ctx, council.Defaults(), m, Conv(hard, 256), func(council.Event) {})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err %v, want context.Canceled", err)
		}
		if d := time.Since(t0); d > 250*time.Millisecond {
			t.Fatalf("returned %v after the start; cancel took too long", d)
		}
		if m.Inflight() != 0 {
			t.Fatalf("%d model calls still running after Run returned", m.Inflight())
		}
	})

	t.Run("member failure cancels its siblings", func(t *testing.T) {
		defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
		m := &stub.Model{Tokens: 200, PerToken: 5 * time.Millisecond, FailRole: council.Researcher, FailIndex: 1}
		t0 := time.Now()
		_, err := newRunner().Run(t.Context(), council.Defaults(), m, Conv(hard, 256), func(council.Event) {})
		if !errors.Is(err, stub.ErrInjected) {
			t.Fatalf("err %v, want the injected failure", err)
		}
		if d := time.Since(t0); d > 250*time.Millisecond {
			t.Fatalf("took %v: the sibling researcher ran on after the failure", d)
		}
		if m.Inflight() != 0 || m.Aborted() == 0 {
			t.Fatalf("inflight %d aborted %d: the sibling was not cancelled", m.Inflight(), m.Aborted())
		}
		if count(m.Calls(), council.Critic) != 0 {
			t.Fatal("critics ran after a researcher failed")
		}
	})

	t.Run("slow consumer loses nothing", func(t *testing.T) {
		m := &stub.Model{Tokens: 50}
		rec := &recorder{delay: 100 * time.Microsecond}
		res, err := newRunner().Run(t.Context(), council.Defaults(), m, Conv(hard, 256), rec.emit)
		if err != nil {
			t.Fatal(err)
		}
		for i := range 2 {
			if got, want := rec.text(council.Thinking, council.Researcher, i, 0), expect(m, council.Researcher, i, 0); got != want {
				t.Fatalf("researcher %d: streamed %d bytes, want %d", i, len(got), len(want))
			}
		}
		if rec.text(council.Content, council.Synthesizer, 0, 0) != res.Answer {
			t.Fatal("answer events lost under a slow consumer")
		}
	})
}

// idealWall is the council's critical path over this stub, measured rather
// than computed: timers overshoot short sleeps, so the ideal is what the
// model calls themselves take when nothing orchestrates them. Decision, plan,
// then one researcher, one critic and the synthesizer back to back.
func idealWall(b *testing.B, m *stub.Model) time.Duration {
	call := func(req council.Request) time.Duration {
		t0 := time.Now()
		if _, err := m.Stream(b.Context(), req, func(string) {}); err != nil {
			b.Fatal(err)
		}
		return time.Since(t0)
	}
	var total time.Duration
	const reps = 20
	for range reps {
		total += call(council.Request{RouteOnly: true, Messages: Conv(hard, 64)})
		total += call(council.Request{Role: council.Planner, WantBriefs: 2})
		for _, r := range []council.Role{council.Researcher, council.Critic, council.Synthesizer} {
			total += call(council.Request{Role: r, Index: 1})
		}
	}
	return total / reps
}

// Bench is the measurement every runner gets.
func Bench(b *testing.B, newRunner func() council.Runner) {
	r := newRunner()
	for _, size := range []int{1 << 10, 64 << 10, 1 << 20} {
		b.Run(fmt.Sprintf("council/state=%dKB", size>>10), func(b *testing.B) {
			m := &stub.Model{}
			conv := Conv(hard, size)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := r.Run(b.Context(), council.Defaults(), m, conv, func(council.Event) {}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	b.Run("direct", func(b *testing.B) {
		m := &stub.Model{}
		conv := Conv("Hello!", 1<<10)
		b.ReportAllocs()
		for b.Loop() {
			if _, err := r.Run(b.Context(), council.Defaults(), m, conv, func(council.Event) {}); err != nil {
				b.Fatal(err)
			}
		}
	})
	for _, w := range []int{3, 8} {
		b.Run(fmt.Sprintf("fanout/width=%d", w), func(b *testing.B) {
			cfg := council.Defaults()
			cfg.Researchers, cfg.Critics = w, w
			m := &stub.Model{Tokens: 20, Prefill: 2 * time.Millisecond, PerToken: 500 * time.Microsecond}
			conv := Conv(hard, 4<<10)
			ideal := idealWall(b, m)
			var total time.Duration
			var peakG int
			for b.Loop() {
				stop := make(chan struct{})
				done := make(chan int)
				go func() {
					p := 0
					for {
						select {
						case <-stop:
							done <- p
							return
						default:
							p = max(p, runtime.NumGoroutine())
							time.Sleep(200 * time.Microsecond)
						}
					}
				}()
				t0 := time.Now()
				if _, err := r.Run(b.Context(), cfg, m, conv, func(council.Event) {}); err != nil {
					b.Fatal(err)
				}
				total += time.Since(t0)
				close(stop)
				peakG = max(peakG, <-done)
			}
			b.ReportMetric(float64(total)/float64(b.N)/float64(ideal), "x-ideal")
			b.ReportMetric(float64(peakG), "peak-goroutines")
		})
	}
	b.Run("ttft", func(b *testing.B) {
		m := &stub.Model{Tokens: 20, Prefill: 2 * time.Millisecond, PerToken: 500 * time.Microsecond}
		var direct, think, answer time.Duration
		for b.Loop() {
			for _, q := range []string{"Hello!", hard} {
				rec := &recorder{}
				t0 := time.Now()
				if _, err := r.Run(b.Context(), council.Defaults(), m, Conv(q, 4<<10), rec.emit); err != nil {
					b.Fatal(err)
				}
				if q == "Hello!" {
					direct += rec.first[council.Content].Sub(t0)
				} else {
					think += rec.first[council.Thinking].Sub(t0)
					answer += rec.first[council.Content].Sub(t0)
				}
			}
		}
		n := float64(b.N) * 1e3
		b.ReportMetric(float64(direct.Microseconds())*1e3/n, "direct-first-content-µs")
		b.ReportMetric(float64(think.Microseconds())*1e3/n, "council-first-thinking-µs")
		b.ReportMetric(float64(answer.Microseconds())*1e3/n, "council-first-content-µs")
	})
}
