package server

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/xollama"
)

// Cerebriline's arithmetic (compaction-shared.ts), at three windows.
func TestCompactionSizesFollowCerebriline(t *testing.T) {
	for _, tc := range []struct {
		window, overhead, reserve int
		want                      compactionSizes
	}{
		{131072, 2000, 3840, compactionSizes{usable: 117964, trigger: 106167, target: 28991, preserve: 17394, lastTurn: 76536}},
		{16384, 500, 3840, compactionSizes{usable: 14745, trigger: 12544, target: 3561, preserve: 2136, lastTurn: 9401}},
		{2048, 160, 3840, compactionSizes{usable: 1843, trigger: 1024, target: 420, preserve: 252, lastTurn: 1110}},
	} {
		z := compactionSizesFor(tc.window, tc.overhead, tc.reserve)
		got := compactionSizes{usable: z.usable, trigger: z.trigger, target: z.target, preserve: z.preserve, lastTurn: z.lastTurn}
		if got != tc.want {
			t.Errorf("window %d: %+v, want %+v", tc.window, got, tc.want)
		}
	}
}

// stockLongReq is a conversation past its size trigger at num_ctx 4096 (2048
// tokens): two exchanges of 600 words a message and a new question.
func stockLongReq(session string) api.ChatRequest {
	return api.ChatRequest{
		Model: "council", SessionID: session,
		Options: map[string]any{"num_ctx": 4096},
		Messages: []api.Message{
			{Role: "user", Content: "What is Rayleigh scattering? " + words(600, "q1")},
			{Role: "assistant", Content: "Light scattered by small particles. " + words(600, "a1")},
			{Role: "user", Content: "Does it depend on wavelength? " + words(600, "q2")},
			{Role: "assistant", Content: "Yes, strongly: the fourth power. " + words(600, "a2")},
			{Role: "user", Content: "Why is the sky blue?"},
		},
	}
}

// routePrompts are the prompts of the council's own decisions, one per turn.
func (e *councilEngine) routePrompts() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for i, r := range e.roles {
		if r == "route" {
			out = append(out, e.prompts[i])
		}
	}
	return out
}

func (e *councilEngine) roleCount(role string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, r := range e.roles {
		if r == role {
			n++
		}
	}
	return n
}

// The flip-flop this phase fixes: once folded, every later turn applies the
// fold; none goes back to the raw history. On stock llama.cpp too: sizing
// needs no /kv.
func TestTheCompactionIsCarriedForward(t *testing.T) {
	councilCompactions.reset()
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilServer(t, e, councilOn())
	req := stockLongReq("conv-carry")
	for turn, q := range []string{"", "And sunsets?", "And the moon?"} {
		if q != "" {
			req = nextTurn(req, q)
		}
		chatChunks(t, s, req)
		councilIdle.Wait()
		p := e.routePrompts()[turn]
		if !strings.Contains(p, compactionSummaryHeading) || !strings.Contains(p, fakeMerged) {
			t.Errorf("turn %d: the council was not sent the summary", turn)
		}
		if strings.Contains(p, "a1 a1 a1") {
			t.Errorf("turn %d: the council was sent the folded answer", turn)
		}
	}
	if n := e.roleCount("compaction-writer"); n != 1 {
		t.Errorf("writer calls over three turns = %d, want the one fold", n)
	}
	if r := councilCompactions.get("conv-carry"); r == nil || r.gen != 1 || r.how != "pooled" && r.how != "continuation" {
		t.Errorf("record %+v, want generation 1 by continuation", r)
	}
}

// A client that edits what the fold replaced gets its own history back, and
// a new first fold; the old summary never covers text it did not see.
func TestAnEditedHistoryDropsTheRecord(t *testing.T) {
	councilCompactions.reset()
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilServer(t, e, councilOn())
	req := stockLongReq("conv-edit")
	chatChunks(t, s, req)
	councilIdle.Wait()
	next := nextTurn(req, "And sunsets?")
	next.Messages[2].Content = "An edited answer. " + words(600, "e1")
	chatChunks(t, s, next)
	councilIdle.Wait()
	r := councilCompactions.get("conv-edit")
	if r == nil || r.gen != 1 || e.roleCount("compaction-writer") != 2 {
		t.Fatalf("record %+v after %d writer calls: want a new first fold", r, e.roleCount("compaction-writer"))
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, rl := range e.roles {
		if rl == "compaction-writer" && strings.Contains(e.prompts[i], "e1 e1") && strings.Contains(e.prompts[i], compactionSummaryHeading) {
			t.Error("the new fold's writer was sent the old summary")
		}
	}
}

// A second fold replays the previous summary with what followed it, and
// carries the user's requests verbatim.
func TestASecondFoldIsIncremental(t *testing.T) {
	councilCompactions.reset()
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilServer(t, e, councilOn())
	req := stockLongReq("conv-incr")
	chatChunks(t, s, req)
	councilIdle.Wait()
	req = nextTurn(req, "Now a long one. "+words(700, "q3"))
	req = nextTurn(req, "And another. "+words(700, "q4"))
	chatChunks(t, s, req)
	councilIdle.Wait()
	r := councilCompactions.get("conv-incr")
	if r == nil || r.gen != 2 {
		t.Fatalf("record %+v, want generation 2", r)
	}
	// The second fold's record still stands for every turn the first folded.
	e.mu.Lock()
	from := len(e.roles)
	e.mu.Unlock()
	chatChunks(t, s, nextTurn(req, "Thanks."))
	councilIdle.Wait()
	e.mu.Lock()
	for i := from; i < len(e.roles); i++ {
		// q3 was folded by the second; quoted, a request is clipped to 2000
		// characters, so whole it can only be the turn itself sent again.
		if e.roles[i] == "route" && (strings.Contains(e.prompts[i], "a1 a1 a1") || strings.Contains(e.prompts[i], words(700, "q3"))) {
			t.Error("a turn the first fold replaced was sent again after the second")
		}
	}
	e.mu.Unlock()
	if !slices.ContainsFunc(r.requests, func(q string) bool { return strings.HasPrefix(q, "What is Rayleigh scattering?") }) {
		t.Error("the first request was not carried into the second fold")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	writers := 0
	for i, rl := range e.roles {
		if rl != "compaction-writer" {
			continue
		}
		if writers++; writers == 2 {
			p := e.prompts[i]
			if !strings.Contains(p, fakeMerged) || strings.Contains(p, "a1 a1 a1") {
				t.Error("the second fold's writer did not read the first summary in place of the folded turns")
			}
		}
	}
}

// The summary is a user message after the system message, which stays as it
// was: the cached prefix up to it survives a fold.
func TestTheSummaryLeadsAndTheSystemStays(t *testing.T) {
	councilCompactions.reset()
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilServer(t, e, councilOn())
	chatChunks(t, s, stockLongReq("conv-layout"))
	councilIdle.Wait()
	p := e.routePrompts()[0]
	sys := strings.Index(p, "<system>")
	sum := strings.Index(p, "<user>"+compactionRequestsHeading)
	tail := strings.Index(p, "Yes, strongly: the fourth power.")
	if sys < 0 || sum < 0 || tail < 0 || sys >= sum || sum >= tail {
		t.Fatalf("layout: system at %d, summary at %d, kept tail at %d", sys, sum, tail)
	}
	if strings.Contains(p[sys:sum], compactionSummaryHeading) {
		t.Error("the summary was written into the system message")
	}
	if !strings.Contains(p[sum:tail], "<user_request>\nWhat is Rayleigh scattering?") {
		t.Error("the folded request is not quoted in the summary")
	}
}

func TestPlanCompaction(t *testing.T) {
	z := compactionSizesFor(4096, 170, 3840)
	z.perChar = 1.0 / 3 // three characters a word, as words() writes them
	msg := func(role string, n int) api.Message { return api.Message{Role: role, Content: words(n, "xx")} }
	sys := api.Message{Role: "system", Content: "s"}

	t.Run("recency", func(t *testing.T) {
		conv := []api.Message{sys, msg("user", 600), msg("assistant", 600), msg("user", 600), msg("assistant", 600), msg("user", 5)}
		p, ok := planCompaction(conv, z, true, 1)
		if !ok || p.cut != 4 || p.pin != -1 {
			t.Errorf("plan %+v %v, want the last exchange kept from the answer on", p, ok)
		}
	})
	t.Run("a question stays with its answer", func(t *testing.T) {
		// After an answer the recency walk stops on the answer alone; the cut
		// comes back to the question it answers.
		conv := []api.Message{sys, msg("user", 600), msg("assistant", 600), msg("user", 5), msg("assistant", 600)}
		p, ok := planCompaction(conv, z, true, 1)
		if !ok || p.cut != 3 {
			t.Errorf("plan %+v %v, want the last question kept with its answer", p, ok)
		}
	})
	t.Run("a quarter of the messages stay", func(t *testing.T) {
		// The last request alone passes the floor, but a quarter of seven
		// messages is two: its answer stays with it.
		conv := []api.Message{sys, msg("user", 50), msg("assistant", 50), msg("user", 50), msg("assistant", 50), msg("user", 50), msg("assistant", 600), msg("user", 600)}
		p, ok := planCompaction(conv, z, true, 1)
		if !ok || p.cut != 6 {
			t.Errorf("plan %+v %v, want the last two messages kept", p, ok)
		}
	})
	t.Run("an oversized last turn is pinned", func(t *testing.T) {
		conv := []api.Message{sys, msg("user", 300), msg("assistant", 300), msg("user", 2600)}
		p, ok := planCompaction(conv, z, true, 1)
		if !ok || p.pin != 3 || p.cut != 4 {
			t.Errorf("plan %+v %v, want the last request pinned and the rest folded", p, ok)
		}
	})
	t.Run("nothing newer than the summary", func(t *testing.T) {
		conv := []api.Message{sys, msg("user", 100), msg("user", 5)}
		if p, ok := planCompaction(conv, z, true, 2); ok {
			t.Errorf("plan %+v folds only the summary", p)
		}
	})
	t.Run("no tail", func(t *testing.T) {
		conv := []api.Message{sys, msg("user", 50), msg("assistant", 50), msg("user", 5)}
		p, ok := planCompaction(conv, z, false, 1)
		if !ok || p.cut != 4 || p.pin != 3 {
			t.Errorf("plan %+v %v, want everything folded but the last request", p, ok)
		}
	})
	t.Run("never after a tool result", func(t *testing.T) {
		conv := []api.Message{sys, msg("user", 5), msg("tool", 5), msg("assistant", 5)}
		if got := safeBoundary(conv, 2, 1); got != 1 {
			t.Errorf("cut at %d, want it walked back off the tool result", got)
		}
	})
}

func TestSplitReplay(t *testing.T) {
	a, b, ok := splitReplay("one two three\n" + compactionHalfway + "\nfour five six")
	if !ok || a != "one two three" || b != "four five six" {
		t.Errorf("at the marker: %q %q %v", a, b, ok)
	}
	// A marker at 10 % is ignored for the blank line nearest the middle.
	a, b, ok = splitReplay("x\n" + compactionHalfway + "\n" + strings.Repeat("y", 40) + "\n\n" + strings.Repeat("z", 40))
	if !ok || strings.Contains(a+b, compactionHalfway) || !strings.HasSuffix(a, "y") {
		t.Errorf("off the band: %q %q %v", a, b, ok)
	}
	if _, _, ok := splitReplay("one line"); ok {
		t.Error("a single line was split")
	}
}

func TestParseCompactionSections(t *testing.T) {
	r, q := parseCompactionSections("noise\n### Replay\nThe replay.\n## retrospective\nThe judgement.")
	if r != "The replay." || q != "The judgement." {
		t.Errorf("%q %q", r, q)
	}
	if r, _ := parseCompactionSections("no headings"); r != "" {
		t.Errorf("no heading read as %q", r)
	}
}

func TestCleanReplay(t *testing.T) {
	if got := cleanReplay("The replay.\nConversation:\n[User]: copied back", true); got != "The replay." {
		t.Errorf("echo not cut: %q", got)
	}
	block := "```\n" + strings.Repeat("x", 3000) + "\n```"
	if got := cleanReplay("Before.\n"+block, true); len(got) > 1400 || !strings.Contains(got, "characters elided") {
		t.Errorf("a long block was not elided: %d characters", len(got))
	}
	if got := cleanReplay("Before.\n"+block, false); !strings.Contains(got, strings.Repeat("x", 3000)) {
		t.Error("a full summary's block was elided")
	}
}

// A merge far shorter than the replay is a synthesizer that lost material:
// the writer's replay stands.
func TestAShortMergeKeepsTheWritersReplay(t *testing.T) {
	councilCompactions.reset()
	e := &councilEngine{route: `{"route":"council"}`, compaction: map[string]string{"compaction-synthesizer": "## Replay\n\nShort."}}
	s := councilServer(t, e, councilOn())
	chatChunks(t, s, stockLongReq("conv-merge"))
	councilIdle.Wait()
	r := councilCompactions.get("conv-merge")
	if r == nil || r.replay != fakeReplayFirst+"\n\n"+fakeReplaySecond {
		t.Errorf("replay %q, want the writer's without its marker", r.replay)
	}
}

// A writer that answers nothing on the conversation is tried three times, then
// on the text path; with nothing there either, the fold is basic.
func TestAWriterThatFailsFallsBack(t *testing.T) {
	for name, tc := range map[string]struct {
		replies map[string]string
		how     string
	}{
		"text":  {map[string]string{"compaction-writer": ""}, "text"},
		"basic": {map[string]string{"compaction-writer": "", "compaction-text-writer": ""}, "basic"},
	} {
		t.Run(name, func(t *testing.T) {
			councilCompactions.reset()
			e := &councilEngine{route: `{"route":"council"}`, compaction: tc.replies}
			s := councilServer(t, e, councilOn())
			chatChunks(t, s, stockLongReq("conv-fallback"))
			councilIdle.Wait()
			r := councilCompactions.get("conv-fallback")
			if r == nil || r.how != tc.how {
				t.Fatalf("record %+v, want %s", r, tc.how)
			}
			if n := e.roleCount("compaction-writer"); n != compactionAttempts {
				t.Errorf("writer attempts = %d, want %d", n, compactionAttempts)
			}
		})
	}
}

// Review off ships the writer's replay; basic makes no model call at all.
func TestCompactionSettings(t *testing.T) {
	no := false
	for name, tc := range map[string]struct {
		ctx     *xollama.CouncilContext
		writers int
		critics int
	}{
		"review off": {&xollama.CouncilContext{Review: &no}, 1, 0},
		"basic":      {&xollama.CouncilContext{Compaction: xollama.CouncilCompactionBasic}, 0, 0},
	} {
		t.Run(name, func(t *testing.T) {
			councilCompactions.reset()
			e := &councilEngine{route: `{"route":"council"}`}
			c := councilOn()
			c.Context = tc.ctx
			s := councilServer(t, e, c)
			chatChunks(t, s, stockLongReq("conv-settings"))
			councilIdle.Wait()
			if r := councilCompactions.get("conv-settings"); r == nil {
				t.Fatal("nothing was folded")
			}
			if w, c := e.roleCount("compaction-writer"), e.roleCount("compaction-critic-1")+e.roleCount("compaction-critic-2"); w != tc.writers || c != tc.critics {
				t.Errorf("writers %d critics %d, want %d %d", w, c, tc.writers, tc.critics)
			}
			e.mu.Lock()
			for i, r := range e.roles {
				if r == "compaction-writer" && strings.Contains(e.prompts[i], "Mark the halfway point") {
					t.Error("the writer was asked for a marker nothing reads")
				}
			}
			e.mu.Unlock()
		})
	}
}

// Where the folded answers carry reasoning, the retrospective judges it and
// leads the summary; with none, it is not asked.
func TestTheRetrospectiveReadsTheFoldedReasoning(t *testing.T) {
	for _, thinking := range []bool{true, false} {
		councilCompactions.reset()
		e := &councilEngine{route: `{"route":"council"}`}
		s := councilServer(t, e, councilOn())
		req := stockLongReq("conv-retro")
		if thinking {
			req.Messages[1].Thinking = "I should start from the law."
		}
		chatChunks(t, s, req)
		councilIdle.Wait()
		asked := e.roleCount("compaction-retrospective") > 0
		r := councilCompactions.get("conv-retro")
		led := r != nil && strings.Contains(r.head[0].Content, compactionRetrospectiveHeading+"\n\n## What worked")
		if asked != thinking || led != thinking {
			t.Errorf("thinking %v: retrospective asked %v, leads the summary %v", thinking, asked, led)
		}
	}
}

// Under refusals an owner above its floor compacts when that lets its booking
// shrink by a quarter (kvPressureCompaction).
func TestRefusalsCompact(t *testing.T) {
	z := compactionSizesFor(65536, 500, 3840)
	for _, tc := range []struct {
		refusing     bool
		grant, floor int
		tokens       int
		want         bool
	}{
		{true, 65536, 4096, 40000, true},
		{false, 65536, 4096, 40000, false}, // nobody refused
		{true, 65536, 65536, 40000, false}, // at its floor
		{true, 65536, 4096, 15000, false},  // not well above what compaction leaves
		{true, 16384, 4096, 12000, false},  // the shrink would give back under a quarter
	} {
		c := &councilCompactor{reserve: 3840, tree: &councilTree{refusing: tc.refusing, grant: tc.grant, floor: tc.floor}}
		zz := z
		if tc.grant == 16384 {
			zz = compactionSizesFor(16384, 500, 3840)
		}
		zz.tokens = tc.tokens
		if got := c.refusalsCompact(zz); got != tc.want {
			t.Errorf("%+v: %v, want %v", tc, got, tc.want)
		}
	}
}

// The writer marks the halfway point only for a review that splits there.
func TestTheWriterMarksTheHalfOnlyForAReview(t *testing.T) {
	councilCompactions.reset()
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilServer(t, e, councilOn())
	chatChunks(t, s, stockLongReq("conv-marker"))
	councilIdle.Wait()
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, r := range e.roles {
		if r == "compaction-writer" && !strings.Contains(e.prompts[i], "Mark the halfway point") {
			t.Error("the writer was not asked to mark the half")
		}
	}
}

// A fold that leaves the conversation no shorter is thrown away: quoting a
// short span's request and replaying it can outweigh the span.
func TestAFoldThatDoesNotShrinkIsDiscarded(t *testing.T) {
	councilCompactions.reset()
	long := words(3000, "r")
	e := &councilEngine{route: `{"route":"council"}`, compaction: map[string]string{
		"compaction-writer": long, "compaction-critic-1": long, "compaction-critic-2": long, "compaction-synthesizer": long,
	}}
	s := councilServer(t, e, councilOn())
	chatChunks(t, s, stockLongReq("conv-small"))
	councilIdle.Wait()
	if e.roleCount("compaction-writer") == 0 {
		t.Fatal("the conversation was not folded at all")
	}
	if r := councilCompactions.get("conv-small"); r != nil {
		t.Errorf("kept a fold that did not shrink the conversation: gen %d", r.gen)
	}
	if _, compacted := e.summaries(); compacted {
		t.Error("the council was sent a fold that did not shrink the conversation")
	}
}

// The window is the owner's grant when it is below num_ctx: a conversation
// short of num_ctx's trigger folds when the engine granted less.
func TestTheGrantSizesTheCompaction(t *testing.T) {
	for _, tc := range []struct {
		grant int
		want  bool
	}{{3072, true}, {16384, false}} {
		councilCompactions.reset()
		councilRoots.reset()
		e := &councilEngine{route: `{"route":"council"}`}
		kv := &fakeKV{grant: tc.grant, most: tc.grant, used: 900, session: "conv-grant"}
		s := polykvCouncil(t, e, kv, councilOn())
		req := longCouncilReq("conv-grant", "Why is the sky blue?")
		req.Options["num_ctx"] = 16384
		for i := range 4 {
			req.Messages[i].Content += " " + words(300, "x")
		}
		chatChunks(t, s, req)
		councilIdle.Wait()
		if _, compacted := e.summaries(); compacted != tc.want {
			t.Errorf("grant %d: compacted %v, want %v", tc.grant, compacted, tc.want)
		}
	}
}

// While the idle council folds, the owner is marked: a next message waits for
// the fold and its root instead of building a second copy beside them.
func TestAnIdleFoldHoldsTheNextTurn(t *testing.T) {
	councilCompactions.reset()
	councilRoots.reset()
	e := &councilEngine{route: `{"route":"council"}`, gate: make(chan struct{})}
	kv := &fakeKV{grant: 16384, used: 900, session: "conv-hold", sessPressure: 0.8}
	s := polykvCouncil(t, e, kv, councilOn())
	chatChunks(t, s, longCouncilReq("conv-hold", "Why is the sky blue?"))
	marked := func() bool {
		councilRoots.mu.Lock()
		defer councilRoots.mu.Unlock()
		_, ok := councilRoots.promoting["conv-hold"]
		return ok
	}
	for range 200 {
		if e.roleCount("compaction-writer") > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !marked() {
		t.Error("the owner was not marked while the idle council folded")
	}
	close(e.gate)
	councilIdle.Wait()
	if marked() {
		t.Error("the mark outlived the idle fold")
	}
}

// The retrospective reads text, not the conversation: it attaches to no pool
// and builds none.
func TestTheRetrospectiveTouchesNoPool(t *testing.T) {
	councilCompactions.reset()
	councilRoots.reset()
	e := &councilEngine{route: `{"route":"council"}`}
	kv := &fakeKV{grant: 16384, used: 900, session: "conv-retro-pool", sessPressure: 0.9}
	s := polykvCouncil(t, e, kv, councilOn())
	req := longCouncilReq("conv-retro-pool", "Why is the sky blue?")
	req.Messages[1].Thinking = "I should start from the law."
	chatChunks(t, s, req)
	councilIdle.Wait()
	if e.roleCount("compaction-retrospective") == 0 {
		t.Fatal("the retrospective was not asked")
	}
	e.mu.Lock()
	for i, r := range e.roles {
		if r == "compaction-retrospective" && e.placements[i] != nil && e.placements[i].PoolID != nil {
			t.Errorf("the retrospective attached pool %d", *e.placements[i].PoolID)
		}
	}
	e.mu.Unlock()
	kv.mu.Lock()
	defer kv.mu.Unlock()
	for _, p := range kv.pools {
		if strings.Contains(p.text, compactionRetrospectiveRole) {
			t.Errorf("a pool was built for the retrospective: %d", p.id)
		}
	}
}
