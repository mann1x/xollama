package council

// The critics review the synthesizer's checks while it keeps working
// (plans/agentic-council-chat.md, 11.9; the owner's design, 2026-09-28): the
// synthesizer sends a check with ReviewTool, the review is queued, a critic
// takes it when free, and every finished review reaches the synthesizer
// before its next model call. Nothing waits on a review except a DONE
// verdict, which stands only once the reviews still out are in.

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ollama/ollama/api"
)

// ReviewTool is the synthesizer's tool for sending a check to the critics.
const ReviewTool = "council_review"

// Reviewer is a critic reviewing a check, on a session of its own.
const Reviewer Role = "reviewer"

// The verdicts a review ends with.
const (
	ReviewConfirmed = "REVIEW: CONFIRMED"
	ReviewRefuted   = "REVIEW: REFUTED"
	ReviewUnclear   = "REVIEW: UNCLEAR"
)

// reviewWait bounds how long a DONE verdict waits for the reviews still out.
var reviewWait = 3 * time.Minute

// WithReview adds ReviewTool to the members' tools. Every member carries it,
// so the prefix they share holds it; only the synthesizer may call it.
func WithReview(tools api.Tools) api.Tools {
	if len(tools) == 0 || slices.ContainsFunc(tools, func(t api.Tool) bool { return t.Function.Name == ReviewTool }) {
		return tools
	}
	props := api.NewToolPropertiesMap()
	props.Set("change", api.ToolProperty{Type: api.PropertyType{"string"}, Description: "What you changed, where, and what the check should show if it worked."})
	return append(append(api.Tools{}, tools...), api.Tool{Type: "function", Function: api.ToolFunction{
		Name:        ReviewTool,
		Description: "Send your last change and the check you ran to the critics. They review it while you keep working, and their reviews reach you as they finish. Only the synthesizer may call it.",
		Parameters:  api.ToolFunctionParameters{Type: "object", Required: []string{"change"}, Properties: props},
	}})
}

// ReviewJob is one check sent for review.
type ReviewJob struct {
	ID      string // the call that sent it, as the synthesizer's key forwards it, and its turn
	Turn    string
	N       int    // its number among the checks sent this turn, from 1
	Request string // the user's latest message
	Change  string // what the synthesizer says it changed
	// Evidence is what its calls returned since the check it sent before:
	// the review judges these, not the synthesizer's account.
	Evidence string
}

// Review is a critic's finished review of a job.
type Review struct {
	Turn   string
	N      int
	Critic int
	Text   string
}

// Reviews is where a turn sends checks and collects reviews. The server
// keeps one per conversation, so a review that finishes while the turn waits
// on the client is there for the next trip.
type Reviews interface {
	// Submit queues a job; a job already sent is not sent again.
	Submit(j ReviewJob) bool
	// Take returns turn's reviews finished since the last Take; a review of
	// an earlier turn's check is dropped.
	Take(turn string) []Review
	// Out is how many jobs are queued or being reviewed.
	Out() int
	// Wait returns once nothing is out, or ctx is done, or d has passed.
	Wait(ctx context.Context, d time.Duration)
	// Sent is how many jobs were ever submitted.
	Sent() int
}

// Desk is the Reviews a critic pool works: one goroutine per critic, taking
// jobs in the order they were sent.
type Desk struct {
	mu      sync.Mutex
	queue   []ReviewJob
	seen    map[string]bool
	busy    int
	done    []Review
	changed chan struct{}
	wake    chan struct{}
}

// NewDesk starts critics reviewers on m, with cfg's critic settings, for as
// long as ctx lasts.
func NewDesk(ctx context.Context, m Model, cfg Config, critics int) *Desk {
	d := &Desk{seen: map[string]bool{}, changed: make(chan struct{}), wake: make(chan struct{}, 1)}
	for i := range max(critics, 1) {
		go d.work(ctx, m, cfg, i)
	}
	return d
}

func (d *Desk) Submit(j ReviewJob) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen[j.ID] {
		return false
	}
	d.seen[j.ID] = true
	d.queue = append(d.queue, j)
	d.signalLocked()
	select {
	case d.wake <- struct{}{}:
	default:
	}
	return true
}

func (d *Desk) Take(turn string) []Review {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Review
	for _, r := range d.done {
		if r.Turn == turn {
			out = append(out, r)
		}
	}
	d.done = nil
	return out
}

func (d *Desk) Out() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.queue) + d.busy
}

func (d *Desk) Sent() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.seen)
}

func (d *Desk) Wait(ctx context.Context, wait time.Duration) {
	deadline := time.After(wait)
	for {
		d.mu.Lock()
		out, ch := len(d.queue)+d.busy, d.changed
		d.mu.Unlock()
		if out == 0 {
			return
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return
		case <-deadline:
			return
		}
	}
}

// signalLocked wakes every Wait.
func (d *Desk) signalLocked() {
	close(d.changed)
	d.changed = make(chan struct{})
}

func (d *Desk) next(ctx context.Context) (ReviewJob, bool) {
	for {
		d.mu.Lock()
		if len(d.queue) > 0 {
			j := d.queue[0]
			d.queue = d.queue[1:]
			d.busy++
			d.mu.Unlock()
			return j, true
		}
		d.mu.Unlock()
		select {
		case <-d.wake:
		case <-ctx.Done():
			return ReviewJob{}, false
		}
	}
}

func (d *Desk) work(ctx context.Context, m Model, cfg Config, i int) {
	for {
		j, ok := d.next(ctx)
		if !ok {
			return
		}
		// Another job may be queued behind this one: let a free mate take it.
		select {
		case d.wake <- struct{}{}:
		default:
		}
		text, err := m.Stream(ctx, reviewRequest(cfg, j, i), func(string) {})
		if err != nil {
			text = fmt.Sprintf("The review could not be made (%v). %s", err, ReviewUnclear)
		}
		d.mu.Lock()
		d.busy--
		if ctx.Err() == nil {
			d.done = append(d.done, Review{Turn: j.Turn, N: j.N, Critic: i, Text: strings.TrimSpace(text)})
		}
		d.signalLocked()
		d.mu.Unlock()
	}
}

// reviewSource heads the reviews a synthesizer gets back; checkSource the
// check a critic reviews.
const (
	reviewSource = "REVIEWS OF YOUR CHECKS BY THE CRITICS"
	checkSource  = "A CHECK THE SYNTHESIZER SENT FOR REVIEW"
)

const reviewInstr = "ROLE: CRITIC %d, REVIEWING A CHECK. The synthesizer is making the changes the user asked for and checking them; it keeps working while you review. Judge only from what its calls returned, not from its account of them: did the change do what it says, did it break something new, and does the check's output show that the change works? Name the line or the output you rely on. At most 120 words, plain text. End with exactly one of: %q (the check shows it works), %q (it does not, or the change broke something) or %q."

// reviewRequest is critic i's review of j: the user's request, the check, and
// the instruction -- short, so a review costs a fraction of a council step.
func reviewRequest(cfg Config, j ReviewJob, i int) Request {
	msgs := []api.Message{{Role: "system"}}
	if j.Request != "" {
		msgs = append(msgs, api.Message{Role: "user", Content: j.Request})
	}
	msgs = append(msgs,
		sourced(checkSource, fmt.Sprintf("What it says it changed: %s\n\nWhat its calls returned since the check it sent before:%s", j.Change, j.Evidence)),
		user(sourcesNote+"\n\n"+fmt.Sprintf(reviewInstr, i+1, ReviewConfirmed, ReviewRefuted, ReviewUnclear)))
	return Request{
		Role: Reviewer, Index: i, Model: cfg.Models[Critic], Host: cfg.Hosts[Critic], Messages: msgs,
		Seed: rand.Int64(), Temperature: cfg.Temperature, MaxTokens: maxTok(cfg, Critic), Think: cfg.Think[Critic],
	}
}

// reviewsMsg hands finished reviews to the synthesizer.
func reviewsMsg(rs []Review) api.Message {
	var b strings.Builder
	b.WriteString("Reviews of the checks you sent, as each finished. They judge what your calls returned:")
	for _, r := range rs {
		fmt.Fprintf(&b, "\n\nREVIEW OF CHECK %d (critic %d):\n%s", r.N, r.Critic+1, r.Text)
	}
	return sourced(reviewSource, b.String())
}

// reviewNote is the synthesizer's instruction about ReviewTool, in a cycle
// that tests.
func (cfg Config) reviewNote(cycle int) string {
	if cfg.Reviews == nil || !cfg.testing(cycle) {
		return ""
	}
	return fmt.Sprintf(" After each check, call %s with what you changed: the critics review it while you keep working, and their reviews reach you as they finish. A %s verdict stands only once the reviews still out are in.", ReviewTool, Done)
}

// reviewedNudge follows the reviews a DONE verdict waited for.
var reviewedNudge = fmt.Sprintf("The critics' reviews of your checks are above. If one shows a check does not prove the change works, keep working. Otherwise reply with %q alone.", Done)

// canReview reports whether r may send checks for review this turn.
func (cfg Config) canReview(r Role) bool { return r == Synthesizer && cfg.Reviews != nil }

// reviewJob is the job a ReviewTool call c sends: the change it states, and
// the synthesizer's calls and results after the check it sent before.
func (cfg Config) reviewJob(key string, turns []api.Message, c api.ToolCall, n int) ReviewJob {
	since := 0
	for i, t := range turns {
		if slices.ContainsFunc(t.ToolCalls, func(x api.ToolCall) bool { return x.Function.Name == ReviewTool && x.ID != c.ID }) {
			since = i + 1
		}
	}
	change := stringArg(c, "change")
	if change == "" {
		change = "It did not say: it declared the work done after this check."
	}
	return ReviewJob{
		ID: cfg.Turn + "/" + ForwardedID(key, c.ID), Turn: cfg.Turn, N: n, Request: cfg.request,
		Change: change, Evidence: cfg.checkEvidence(key, turns[since:]),
	}
}

// checkEvidence is each call a check made and what it returned, whole to
// maxEvidence: a reviewer has no tools to read a ref back with.
func (cfg Config) checkEvidence(key string, turns []api.Message) string {
	var b strings.Builder
	for _, t := range turns {
		for _, c := range t.ToolCalls {
			if local(c) {
				continue
			}
			res, _, ok := cfg.result(key, c)
			if !ok {
				continue
			}
			if n := len(res); n > maxEvidence {
				res = truncate(res, maxEvidence) + fmt.Sprintf("\n[... %d more characters]", n-len(truncate(res, maxEvidence)))
			}
			fmt.Fprintf(&b, "\n- %s %s returned:\n%s", c.Function.Name, c.Function.Arguments.String(), res)
		}
	}
	if b.Len() == 0 {
		return "\n(no calls since)"
	}
	return b.String()
}

// sendForReview queues the ReviewTool calls of the synthesizer's last turn.
func (cfg Config) sendForReview(r Role, key string, turns []api.Message) {
	if !cfg.canReview(r) || len(turns) == 0 {
		return
	}
	for _, c := range turns[len(turns)-1].ToolCalls {
		if c.Function.Name == ReviewTool {
			cfg.Reviews.Submit(cfg.reviewJob(key, turns, c, cfg.Reviews.Sent()+1))
		}
	}
}

// reviewedLast reports whether the synthesizer's last client call was
// followed by a check it sent for review.
func reviewedLast(turns []api.Message) bool {
	for i := len(turns) - 1; i >= 0; i-- {
		for _, c := range turns[i].ToolCalls {
			if c.Function.Name == ReviewTool {
				return true
			}
			if !local(c) {
				return false
			}
		}
	}
	return false
}
