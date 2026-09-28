package council

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
)

// reviewStub's synthesizer changes a file, sends the check for review, and
// declares the work done without waiting. Its reviewer holds its review until
// the synthesizer's next call has begun -- so a review that blocked the
// synthesizer would hang the test -- then refutes it. Everyone else is
// toolStub.
type reviewStub struct {
	toolStub
	skipReview bool      // the synthesizer never calls ReviewTool
	began      chan bool // closed when the synthesizer calls after sending
	once       sync.Once
	reviews    []Request
}

func (s *reviewStub) Stream(ctx context.Context, req Request, onToken func(string)) (string, error) {
	if req.Role != Reviewer {
		return s.toolStub.Stream(ctx, req, onToken)
	}
	s.mu.Lock()
	s.reviews = append(s.reviews, req)
	s.mu.Unlock()
	if !s.skipReview {
		select {
		case <-s.began:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "The page still throws at line 119. " + ReviewRefuted, nil
}

func (s *reviewStub) StreamTools(ctx context.Context, req Request, onToken func(string)) (Reply, error) {
	if req.Role != Synthesizer {
		return s.toolStub.StreamTools(ctx, req, onToken)
	}
	s.mu.Lock()
	s.calls = append(s.calls, req)
	s.mu.Unlock()
	var calls []string
	reviewed := false
	for _, m := range req.Messages {
		for _, c := range m.ToolCalls {
			calls = append(calls, c.Function.Name)
		}
		if strings.HasPrefix(m.Content, header(reviewSource)) {
			reviewed = true
		}
	}
	call := func(name string, args map[string]any) (Reply, error) {
		a := api.NewToolCallFunctionArguments()
		for k, v := range args {
			a.Set(k, v)
		}
		return Reply{Calls: []api.ToolCall{{Function: api.ToolCallFunction{Name: name, Arguments: a}}}}, nil
	}
	switch {
	case len(calls) == 0:
		return call("write_file", map[string]any{"path": "game.html"})
	case len(calls) == 1 && !s.skipReview:
		return call(ReviewTool, map[string]any{"change": "removed the brace at line 119"})
	case !reviewed:
		s.once.Do(func() { close(s.began) })
		onToken("fixed it ")
		return Reply{Content: "fixed it " + Done}, nil
	}
	// The reviews are in: the verdict stands. What it says with it is
	// unseen -- the user has read the answer.
	onToken("the review is noted ")
	return Reply{Content: "the review is noted " + Done}, nil
}

func reviewCfg(t *testing.T, s *reviewStub) Config {
	cfg := toolCfg()
	cfg.Tools = WithReview(cfg.Tools)
	cfg.Reviews = NewDesk(t.Context(), s, cfg, cfg.Critics)
	return cfg
}

// The synthesizer sends its check and keeps working; the critic's review
// reaches it before its DONE stands, and the answer is the one the user read.
func TestTheCriticsReviewAChecksWhileTheSynthesizerWorks(t *testing.T) {
	s := &reviewStub{toolStub: toolStub{stub: stub{route: `{"route":"council"}`}}, began: make(chan bool)}
	cfg := reviewCfg(t, s)
	var content strings.Builder
	res, err := Run(t.Context(), cfg, s, conv, func(e Event) {
		if e.Kind == Content {
			content.WriteString(e.Text)
		}
	})
	for trip := 0; err == nil && len(res.Calls) > 0; trip++ {
		cfg.Results = map[string]string{res.Calls[0].ID: "WROTE game.html"}
		res, err = RunFrom(t.Context(), cfg, s, conv, res.Progress, nil, func(e Event) {
			if e.Kind == Content {
				content.WriteString(e.Text)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(s.reviews) != 1 {
		t.Fatalf("%d reviews made, want 1", len(s.reviews))
	}
	got := all(s.reviews[0])
	if !strings.Contains(got, "removed the brace at line 119") || !strings.Contains(got, "write_file") || !strings.Contains(got, "WROTE game.html") || !strings.Contains(got, header(checkSource)) {
		t.Errorf("the reviewer did not read the change and what the calls returned:\n%s", got)
	}
	last := lastOf(s.calls, Synthesizer)
	if !strings.Contains(all(last), header(reviewSource)) || !strings.Contains(all(last), "REVIEW OF CHECK 1 (critic 1)") || !strings.Contains(all(last), ReviewRefuted) {
		t.Errorf("the synthesizer did not get the review before its verdict stood")
	}
	if res.Answer != "fixed it" || content.String() != "fixed it " {
		t.Errorf("answer %q, content %q: want the answer the user read, once", res.Answer, content.String())
	}
}

// A DONE after a check it never sent sends that check itself, and waits.
func TestADoneSendsItsLastCheckForReview(t *testing.T) {
	s := &reviewStub{toolStub: toolStub{stub: stub{route: `{"route":"council"}`}}, skipReview: true, began: make(chan bool)}
	cfg := reviewCfg(t, s)
	res := drive(t, cfg, s)
	if len(s.reviews) != 1 || !strings.Contains(all(s.reviews[0]), "declared the work done") {
		t.Fatalf("%d reviews, want the DONE's own", len(s.reviews))
	}
	if !strings.Contains(all(lastOf(s.calls, Synthesizer)), ReviewRefuted) || res.Answer != "fixed it" {
		t.Errorf("answer %q", res.Answer)
	}
}

// The desk works its queue in order, sends a job once, and keeps a turn's
// reviews from another turn's synthesizer.
func TestTheDeskWorksItsQueue(t *testing.T) {
	var mu sync.Mutex
	var order []string
	m := modelFunc(func(ctx context.Context, req Request, _ func(string)) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		for _, msg := range req.Messages {
			if strings.HasPrefix(msg.Content, header(checkSource)) {
				order = append(order, msg.Content[strings.Index(msg.Content, ": ")+2:strings.Index(msg.Content, "\n\n")])
			}
		}
		return "ok " + ReviewConfirmed, nil
	})
	d := NewDesk(t.Context(), m, toolCfg(), 1)
	for i, change := range []string{"a", "b", "c"} {
		if !d.Submit(ReviewJob{ID: change, Turn: "t1", N: i + 1, Change: change}) {
			t.Fatal("a new job was refused")
		}
	}
	if d.Submit(ReviewJob{ID: "a", Turn: "t1"}) {
		t.Error("a job was sent twice")
	}
	d.Submit(ReviewJob{ID: "z", Turn: "t0", N: 4, Change: "z"})
	d.Wait(t.Context(), 5*time.Second)
	if d.Out() != 0 || strings.Join(order, "") != "abcz" {
		t.Fatalf("out %d, order %q", d.Out(), order)
	}
	rs := d.Take("t1")
	if len(rs) != 3 || rs[0].N != 1 || rs[2].N != 3 {
		t.Errorf("reviews %+v", rs)
	}
	if len(d.Take("t1")) != 0 {
		t.Error("a review was handed over twice")
	}
}

// Only the synthesizer sends checks for review.
func TestOnlyTheSynthesizerSendsChecksForReview(t *testing.T) {
	cfg := toolCfg()
	cfg.Tools = WithReview(cfg.Tools)
	c := api.ToolCall{Function: api.ToolCallFunction{Name: ReviewTool}}
	for _, r := range []Role{Researcher, Critic, Front, Planner} {
		if ok, _ := cfg.may(r, c); ok {
			t.Errorf("%s may call %s", r, ReviewTool)
		}
	}
	if ok, _ := cfg.may(Synthesizer, c); !ok {
		t.Error("the synthesizer may not")
	}
}

type modelFunc func(ctx context.Context, req Request, onToken func(string)) (string, error)

func (f modelFunc) Stream(ctx context.Context, req Request, onToken func(string)) (string, error) {
	return f(ctx, req, onToken)
}

// movingOnStub's synthesizer changes the file, checks it, changes it again
// without sending the check, then declares the work done.
type movingOnStub struct {
	reviewStub
	next []string // the synthesizer's calls, in order, before its DONE
}

func (s *movingOnStub) StreamTools(ctx context.Context, req Request, onToken func(string)) (Reply, error) {
	if req.Role != Synthesizer {
		return s.toolStub.StreamTools(ctx, req, onToken)
	}
	s.mu.Lock()
	s.calls = append(s.calls, req)
	s.mu.Unlock()
	n := 0
	for _, m := range req.Messages {
		n += len(m.ToolCalls)
	}
	next := s.next
	if n < len(next) {
		c := readCall("", "game.html")
		c.Function.Name = next[n]
		return Reply{Calls: []api.ToolCall{c}}, nil
	}
	return Reply{Content: "fixed it " + Done}, nil
}

// A check the synthesizer moved on from without sending is sent for it, once,
// and the reviews reach the deliberation as the reviewer's thinking.
func TestACheckLeftUnsentIsSentForTheSynthesizer(t *testing.T) {
	s := &movingOnStub{reviewStub: reviewStub{toolStub: toolStub{stub: stub{route: `{"route":"council"}`}}, skipReview: true, began: make(chan bool)}, next: []string{"write_file", "read_files", "write_file"}}
	cfg := reviewCfg(t, &s.reviewStub)
	cfg.Reviews = NewDesk(t.Context(), s, cfg, cfg.Critics)
	var thinking []Event
	emit := func(e Event) {
		if e.Role == Reviewer && e.Kind == Thinking && !e.Done {
			thinking = append(thinking, e)
		}
	}
	names := []string{"write_file", "read_files", "write_file"}
	res, err := Run(t.Context(), cfg, s, conv, emit)
	for trip := 0; err == nil && len(res.Calls) > 0; trip++ {
		cfg.Results = map[string]string{res.Calls[0].ID: "RESULT " + names[min(trip, 2)]}
		res, err = RunFrom(t.Context(), cfg, s, conv, res.Progress, nil, emit)
	}
	if err != nil {
		t.Fatal(err)
	}
	var auto int
	for _, r := range s.reviews {
		if strings.Contains(all(r), "sent this check for it when it moved on") {
			auto++
		}
	}
	if auto != 1 {
		t.Fatalf("%d checks sent on moving on, want 1 (of %d reviews)", auto, len(s.reviews))
	}
	if len(thinking) == 0 || !strings.HasPrefix(thinking[0].Text, "Review of check 1: ") || thinking[0].Index != 0 {
		t.Errorf("the review was not streamed as the reviewer's thinking: %+v", thinking)
	}
}

// A DONE after a check already sent on moving on does not send it again.
func TestACheckIsReviewedOnce(t *testing.T) {
	s := &movingOnStub{reviewStub: reviewStub{toolStub: toolStub{stub: stub{route: `{"route":"council"}`}}, skipReview: true, began: make(chan bool)}, next: []string{"write_file", "read_files", "read_files"}}
	cfg := reviewCfg(t, &s.reviewStub)
	cfg.Reviews = NewDesk(t.Context(), s, cfg, cfg.Critics)
	drive(t, cfg, s)
	if len(s.reviews) != 1 {
		t.Errorf("%d reviews of one check", len(s.reviews))
	}
}
