package council

// Tools on council turns (plans/agentic-council-chat.md, 9.5). Every member's
// prompt carries the client's tools, so the shared prefix holds; which member
// may call which tool is decided here:
//
//   - the synthesizer, and the planner answering directly, answer the user:
//     they may call every tool;
//   - researchers and critics may call only the tools the client marked
//     read-only (api.ToolFunction.ReadOnly). Parallel members editing one
//     file would lose each other's edits;
//   - the route decision and the plan follow a JSON schema and call nothing.
//
// A member whose reply calls a tool it may call is suspended: its turns so far
// go into the turn's Progress, its calls to the client under an id naming the
// member ("r2:call_0"), and the next request brings the results back. A call
// it may not make is answered in place with a refusal, and never leaves.

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/ollama/ollama/api"
)

// Reply is a member's whole reply: its text, and the tools it calls.
type Reply struct {
	Content string
	// Thinking is the reasoning the reply followed, replayed on the member's
	// next step (replay.go).
	Thinking string
	Calls    []api.ToolCall
	// Cut is a reply the reply cap ended (done_reason "length", cut.go).
	Cut bool
}

// ToolModel is a Model whose members can call the client's tools. The
// council's calls go through StreamTools when the turn has tools; a Model
// without it answers as before, and its members call nothing.
type ToolModel interface {
	Model
	StreamTools(ctx context.Context, req Request, onToken func(string)) (Reply, error)
}

// MemberKey names a member within a turn, as the ids of the calls it forwards
// do: r1, c2 (a researcher, a critic; ".2" from the second round on), s for
// the synthesizer and d for the planner's direct answer.
func MemberKey(r Role, index, round int) string {
	var k string
	switch r {
	case Researcher:
		k = fmt.Sprintf("r%d", index+1)
	case Critic:
		k = fmt.Sprintf("c%d", index+1)
	case Synthesizer:
		k = "s"
	case Front:
		k = "f"
	default:
		k = "d"
	}
	if round > 0 {
		k += fmt.Sprintf(".%d", round+1)
	}
	return k
}

// writes reports whether a role answers the user, and so may change things.
func writes(r Role) bool { return r == Synthesizer || r == Planner || r == Front }

// maxStanding bounds how often a DONE is refused for a refutation that
// stands; after that the member's verdict is taken.
const maxStanding = 2

// maxRefusals bounds how often a member that calls only tools it may not is
// asked again; after that its text stands.
const maxRefusals = 2

const (
	refusedWrite = "Refused: %s can change things, and only the synthesizer calls such tools. Say in your reply what should change instead."
	refusedName  = "Refused: there is no tool named %s."
	// refusedRouting answers a member other than the front that calls one
	// of its tools: the council is already working on the request.
	refusedRouting = "Refused: %s is the synthesizer's, when it takes a request; you are already working on this one."
	noResult       = "(no result came back for this call)"
	// repeatedCall follows a result that repeats, word for word, the one an
	// earlier call with the same arguments got. Measured on eleven2go on
	// b177: a synthesizer sent the same failing edit 15 times in one turn.
	repeatedCall = "\n\n[council: the same call with the same arguments as %s, and the same result. Repeating it will not change the outcome: re-read what it acts on, or try something different.]"
)

func (cfg Config) tool(name string) (api.Tool, bool) {
	i := slices.IndexFunc(cfg.Tools, func(t api.Tool) bool { return t.Function.Name == name })
	if i < 0 {
		return api.Tool{}, false
	}
	return cfg.Tools[i], true
}

// may reports whether role may call c; the reason is the refusal otherwise.
func (cfg Config) may(r Role, c api.ToolCall) (bool, string) {
	t, ok := cfg.tool(c.Function.Name)
	switch {
	case !ok:
		return false, fmt.Sprintf(refusedName, c.Function.Name)
	case routing(c.Function.Name) && r != Front:
		return false, fmt.Sprintf(refusedRouting, c.Function.Name)
	case c.Function.Name == ReviewTool && r != Synthesizer:
		return false, fmt.Sprintf(refusedRouting, c.Function.Name)
	case resultTool(c.Function.Name) && resultRole(c.Function.Name) != r:
		return false, fmt.Sprintf(refusedRouting, c.Function.Name)
	case !t.Function.ReadOnly && !writes(r):
		return false, fmt.Sprintf(refusedWrite, c.Function.Name)
	case !t.Function.ReadOnly && noOp(c):
		return false, noChange
	}
	return true, ""
}

// transcript is a member's own turns after its instruction: each of its
// assistant turns, then one result per call -- the client's, or the refusal.
func (cfg Config) transcript(r Role, key string, turns []api.Message) []api.Message {
	var out []api.Message
	folded := cfg.folded(r, key, turns)
	sent := cfg.sends(key, turns)
	misq := cfg.misquotes(r, key, turns)
	seen := map[string]string{}
	for _, t := range turns {
		out = append(out, t)
		for _, c := range t.ToolCalls {
			res := noResult
			if ok, why := cfg.may(r, c); !ok {
				res = why
			} else if h, ok := misq[c.ID]; ok {
				res = h
			} else if c.Function.Name == PostTool {
				res = cfg.postAnswer(r, key, c)
			} else if c.Function.Name == RebuildTool {
				res = cfg.rebuilt()
			} else if c.Function.Name == ForwardTool {
				res = "Handed to the council."
			} else if c.Function.Name == ReviewTool {
				res = "Sent to the critics. Keep working: their review reaches you when it is done."
			} else if resultTool(c.Function.Name) {
				// Taken only as a turn's one call (takeResult): beside others
				// it waits for their results.
				res = resultLater
			} else if local(c) {
				res = cfg.lookup(c)
				if folded[c.ID] {
					res = droppedLookup
				}
			} else if s, ref, ok := cfg.result(key, c); ok {
				res = s
				if folded[c.ID] {
					res = indexed(ref, s)
				}
				same := readKey(c) + "\x00" + s
				if n := sent[c.ID]; n >= 2 {
					res += strikeNote(n)
				} else if first, ok := seen[same]; ok {
					res += fmt.Sprintf(repeatedCall, first)
				} else {
					seen[same] = c.ID
				}
			}
			out = append(out, api.Message{Role: "tool", Content: res, ToolName: c.Function.Name, ToolCallID: c.ID})
		}
	}
	return out
}

// ForwardedID is the id a member's call carries to the client.
func ForwardedID(key, id string) string { return key + ":" + id }

// named gives every call of a member's turn an id, unique within the turn's
// transcript, for a model that sent none.
func named(calls []api.ToolCall, turn int) []api.ToolCall {
	out := slices.Clone(calls)
	for i := range out {
		if out[i].ID == "" {
			out[i].ID = fmt.Sprintf("call_%d_%d", turn, i)
		}
	}
	return out
}

// forwarded is what the client runs for a suspended member: the calls of its
// last turn it may make, under ids naming it.
func (cfg Config) forwarded(r Role, key string, turns []api.Message) []api.ToolCall {
	if len(turns) == 0 {
		return nil
	}
	var out []api.ToolCall
	misq := cfg.misquotes(r, key, turns)
	for _, c := range turns[len(turns)-1].ToolCalls {
		if _, answered := misq[c.ID]; answered {
			continue
		}
		if ok, _ := cfg.may(r, c); ok && !local(c) && !cfg.cachedRead(c) {
			c.ID = ForwardedID(key, c.ID)
			out = append(out, c)
		}
	}
	return out
}

// toolCharter joins the charter on a turn with tools. Measured live on b137
// without it: the charter's "using only the conversation and their own
// knowledge" held, researchers called nothing and described calls they had
// not made, and the synthesizer asked leave to write instead of writing.
const toolCharter = `The user's tools are listed at the start; they act on the user's real environment. Researchers and critics call the tools that only read, to check facts instead of guessing. The synthesizer, and the planner when it answers directly, may call every tool, and make the changes the user asked for. A tool's result arrives in a message of role tool: never describe a call you did not make or a result you did not receive.`

// charterNoTools is the built-in charter's sentence a turn with tools must
// not keep: it forbids the very reading the tools are for.
const (
	charterNoTools   = "using only the conversation and their own knowledge"
	charterWithTools = "using the conversation, their own knowledge and the tools that only read"
)

// charter is the council's standing instruction for this turn, followed by
// the guidance every role reads (directive.go): in the shared prefix, so
// PolyKV holds it once.
func (cfg Config) charter() string {
	return cfg.withInstructions(cfg.baseCharter(), Everyone)
}

func (cfg Config) baseCharter() string {
	switch {
	case len(cfg.Tools) == 0:
		return cfg.Charter
	case cfg.Charter == "":
		return toolCharter
	}
	return strings.Replace(cfg.Charter, charterNoTools, charterWithTools, 1) + "\n\n" + toolCharter
}

// toolNote tells a member what the tools are for in its role. It goes in the
// role's instruction, after the shared prefix.
func (cfg Config) toolNote(r Role) string {
	if len(cfg.Tools) == 0 {
		return ""
	}
	if writes(r) {
		// Measured live: a synthesizer after the plan's JSON once answered
		// with a plan of its own and changed nothing.
		return " Make the changes the user asked for by calling the tools, then write the answer for the user. The plan above is the council's, not a format to follow." + cfg.lookupNote(r)
	}
	var ro []string
	for _, t := range cfg.Tools {
		if t.Function.ReadOnly && t.Function.Name != EvidenceTool && t.Function.Name != PostTool && t.Function.Name != ReviewTool {
			ro = append(ro, t.Function.Name)
		}
	}
	// Measured live: a critic after the plan's JSON answered in JSON too.
	const prose = " Reply in plain text, not JSON."
	if len(ro) == 0 {
		return " Do not call tools: they change things, and only the synthesizer calls them." + prose
	}
	// A step is a round trip to the client, so reads go together (claude-hooks
	// P2): the critic of 20260930-085958 made 50 finds, one per trip.
	note := fmt.Sprintf(" You may call these tools, which only read: %s. You have %d tool steps and each one is a round trip, so put every read you need now into one step, then write your report when their results are in. The others change things, and only the synthesizer calls them.%s", strings.Join(ro, ", "), cfg.stepLimit(r), prose)
	if r == Researcher && slices.ContainsFunc(cfg.Tools, func(t api.Tool) bool {
		return !t.Function.ReadOnly && t.Function.Name != EvidenceTool && t.Function.Name != PostTool && t.Function.Name != ReviewTool && !routing(t.Function.Name)
	}) {
		// Researchers propose, the synthesizer tests (11.4): ab-4's
		// researchers reported a diagnosis nobody checked.
		// Every fault it finds, not the first: one fault per report cost a
		// whole check cycle per fault on medium (1101 s against plain's 91).
		note += " When the task needs changes, propose every one you find in your part, not only the first: for each, what to change, where, and how to check that it worked. The synthesizer makes them together and checks them." + wholeNote
	}
	if r == Critic && slices.ContainsFunc(cfg.Tools, func(t api.Tool) bool {
		return !t.Function.ReadOnly && t.Function.Name != EvidenceTool && t.Function.Name != PostTool && t.Function.Name != ReviewTool && !routing(t.Function.Name)
	}) {
		// Measured in ab-5: critics called edit_file three times to test a
		// proposal, and were refused each time.
		note += " You cannot make or test a change: judge each proposal against the material and the evidence."
	}
	if cfg.canReport() {
		// The result is a record (report.go): fields the runner reads, not a
		// marker the model has to remember.
		switch r {
		case Researcher:
			note += fmt.Sprintf(" When your reads are done, call %s once, on its own, with your summary, every change you propose (the exact old text from what you read, and the new text) and what you established: that is your report.", ReportTool)
		case Critic:
			note += fmt.Sprintf(" End by calling %s once, on its own, with your verdict: ready, revise or confirmed.", VerdictTool)
		}
	}
	if r == Critic {
		// Measured live: critics re-read every file the researchers had read,
		// and one on 20260930-085958 turned researcher: 50 finds in 26 trips.
		// A check, not a survey (claude-hooks P3).
		note += " The findings carry the tool results the researchers read. Check only the claims the answer depends on; if a claim cannot be checked from the findings, say so in your verdict instead of investigating it."
	}
	return note + cfg.lookupNote(r) + cfg.postNote(r)
}

// replyText is a member's reply over its turns: its last text, or -- when
// it ended on a call with nothing more to say -- what it wrote before the
// calls. The narration before each call ("let me read the file") is not the
// reply.
func replyText(turns []api.Message, last string) string {
	if strings.TrimSpace(last) != "" || len(turns) == 0 {
		return last
	}
	var parts []string
	for _, t := range turns {
		if t.Role != "assistant" {
			continue // a mate's notes (broadcast.go), not the member's words
		}
		if s := strings.TrimSpace(t.Content); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}

// maxEvidence caps one tool result carried in a reply's evidence.
const maxEvidence = 16000

// evidence is what a researcher or critic read, carried in its reply to every
// member after it: each call it made and the client's result. Measured live
// on b137 without it: the synthesizer -- the only member that may write --
// never saw a file it was asked to edit, and either read it again or claimed
// an edit it never made; critics re-read what the researchers had read.
func (cfg Config) evidence(r Role, key string, turns []api.Message) string {
	return cfg.evidenceOf(r, key, turns, nil)
}

// evidenceOf is evidence of the calls keep takes, by their message's index in
// the member's transcript; a nil keep takes every call.
func (cfg Config) evidenceOf(r Role, key string, turns []api.Message, keep func(at int, c api.ToolCall) bool) string {
	var b strings.Builder
	for at, m := range cfg.transcript(r, key, turns) {
		if m.Role != "assistant" {
			continue
		}
		for _, c := range m.ToolCalls {
			res, ref, ok := cfg.result(key, c)
			if ok2, _ := cfg.may(r, c); !ok || !ok2 || local(c) || (keep != nil && !keep(at, c)) {
				continue
			}
			if b.Len() == 0 {
				b.WriteString("\n\nEvidence (the tools called and what they returned):")
			}
			if ref != ForwardedID(key, c.ID) {
				// A shared read: the other member's evidence carries it.
				fmt.Fprintf(&b, "\n- %s %s returned the same as %s.", c.Function.Name, c.Function.Arguments.String(), ref)
				continue
			}
			if len(res) > inlineEvidence && cfg.canLookup() {
				res = indexed(ref, res)
			} else if n := len(res); n > maxEvidence {
				res = truncate(res, maxEvidence) + fmt.Sprintf("\n[... %d more characters]", n-len(truncate(res, maxEvidence)))
			}
			fmt.Fprintf(&b, "\n- %s %s returned:\n%s", c.Function.Name, c.Function.Arguments.String(), res)
		}
	}
	return b.String()
}

// narrated reports whether a reply describes calling a tool the member may
// call without calling it: measured live on b137, 6 of 72 researchers'
// first replies wrote "I have listed the files ... list_files returned ..."
// and invented the result, and the synthesizer answered from the invention.
// A plain chat of the same model never did.
func (cfg Config) narrated(r Role, reply string) bool {
	for _, t := range cfg.Tools {
		if (t.Function.ReadOnly || writes(r)) && strings.Contains(reply, t.Function.Name) {
			return true
		}
	}
	return false
}

// narratedNudge is what a member that narrated a call is told, once.
const narratedNudge = "You wrote about calling tools without calling them, so their results are not in your report. Call the tools now; write the report only from results you received."

// callTools runs one member that may call tools, from its turns so far. It
// returns the reply, or -- when the member calls a tool it may call -- its
// turns, to be suspended until the client's results come back.
func callTools(ctx context.Context, tm ToolModel, cfg Config, req Request, turns []api.Message, onToken func(string)) (string, []api.Message, error) {
	key := MemberKey(req.Role, req.Index, req.Round)
	own := req.Messages
	req.MaxTokens = writeTok(req.Role, req.MaxTokens)
	refusals, lookups, preempts, cuts, standing := 0, 0, 0, 0, 0
	nudged, gated, emptied, typed, unformatted := false, false, false, false, false
	// gatedReply is the DONE reply that waited for the critics' reviews
	// (11.9): the user has read it.
	gatedReply, stream := "", onToken
	// unverdicted is the reply that ended without a verdict (C): the user
	// has read it, so the nudged reply only adds the verdict to it, unseen.
	unverdicted := ""
	// held is the DONE reply the user read when a standing refutation sent
	// the member back: its answer, unless it goes back to work.
	held := ""
	for {
		if cfg.CheckCall == nil {
			// No harness stated the check: the one this member repeats is it
			// (generic.go), from the moment it repeats one.
			cfg.CheckCall = cfg.inferredCheck(turns)
		}
		if n := cfg.unread(req.Role, key); n != nil {
			// Into the member's own turns, so its prefix stays and a resume
			// reads them where they were.
			turns = append(slices.Clone(turns), *n)
		}
		if cfg.canReview(req.Role) {
			// 11.9: the reviews finished since its last call, as they landed.
			if rs := cfg.Reviews.Take(cfg.Turn); len(rs) > 0 {
				cfg.show(req.Round, rs)
				turns = append(slices.Clone(turns), reviewsMsg(rs))
			}
		}
		if writes(req.Role) && cfg.struck(key, turns) {
			// The same change sent loopStrikes times with the same result:
			// the member's steps end here, with the last send's result in.
			out := replyText(turns, "") + "\n\n" + strikeStop
			if req.Role == Synthesizer && cfg.testing(req.Round) {
				cfg.recordCheck(key, turns)
				out = replyText(turns, "") + "\n\n" + Retest + " " + strikeStop
			}
			return out + cfg.evidence(req.Role, key, turns), nil, nil
		}
		if req.Role == Synthesizer && cfg.testing(req.Round) {
			// D: a cycle's steps are bounded; past them the synthesizer is
			// told to report, and two steps later its report is taken as is.
			switch steps := toolSteps(turns); {
			case steps >= cfg.MaxSteps+2:
				cfg.recordCheck(key, turns)
				out := replyText(turns, "") + "\n\n" + Retest + " The cycle's tool steps ran out before a check passed."
				return out + cfg.evidence(req.Role, key, turns), nil, nil
			case steps >= cfg.MaxSteps && !noted(turns, budgetNote(cfg.MaxSteps)):
				turns = append(slices.Clone(turns), user(budgetNote(cfg.MaxSteps)))
			}
		}
		// At its step bound a reader's call carries its result's schema as the
		// format: the grammar admits no tool call, and the tool list -- the
		// shared prefix -- stays as it was.
		req.Format = nil
		if lim := cfg.stepLimit(req.Role); lim > 0 && toolSteps(turns) >= lim && cfg.canReport() && !unformatted {
			if !noted(turns, stepsOutNote) {
				turns = append(slices.Clone(turns), user(stepsOutNote))
			}
			req.Format = resultSchema(req.Role)
		}
		turns = cfg.condense(ctx, tm, key, turns)
		req.Messages = append(clone(own), cfg.transcript(req.Role, key, turns)...)
		rep, partial, preempted, err := cfg.streamPreemptible(ctx, tm, req, key, onToken)
		if preempted && preempts < maxPreempts {
			// A mate's verdict interrupted it: what it had written stays as
			// its turn, and the note is read before it goes on.
			preempts++
			if strings.TrimSpace(partial) != "" {
				turns = append(slices.Clone(turns), api.Message{Role: "assistant", Content: partial})
			}
			onToken("\n\n(interrupted: a mate's checked result)\n\n")
			continue
		}
		if err != nil && req.Format != nil && !unformatted && ctx.Err() == nil {
			// An engine that refuses the result's format (live, 0415: a
			// grammar it could not parse) must not end the member: it is
			// asked once more without it, the note still in its turns.
			unformatted = true
			slog.Info("council: the forced answer's format was refused; asking without it", "member", key, "error", err)
			continue
		}
		if err != nil && fallsBack(ctx, req) {
			// As in call: one of several researchers or critics elsewhere is
			// answered by the council's own model.
			onToken(fmt.Sprintf("\n\n(%s on %s failed; the council's model answers instead)\n\n", req.Role, memberWhere(req)))
			req.Model, req.Host = "", ""
			rep, err = tm.StreamTools(ctx, req, onToken)
		}
		if err != nil {
			return "", nil, err
		}
		if wasCut(req.Role, rep) && cuts < maxCuts {
			// The call it was writing is lost: asked again, for less at once.
			cuts++
			turns = append(slices.Clone(turns), cutTurn())
			onToken("\n\n(cut at the reply limit: asked again for a smaller change)\n\n")
			continue
		}
		cuts = 0
		if req.Format != nil && len(rep.Calls) == 0 {
			// The forced answer: its record, or its text as it stands.
			if out, ok := cfg.formatted(req.Role, key, turns, rep.Content); ok {
				return out, nil, nil
			}
		}
		if cfg.takeResult(req.Role, req.Round, key, turns, &rep) {
			typed = true
		}
		if len(rep.Calls) == 0 && !writes(req.Role) && !emptied && req.Think != "" && strings.TrimSpace(replyText(turns, rep.Content)) == "" {
			// A report its reasoning took whole is asked for once more
			// without thinking (claude-hooks P4): a researcher on
			// 20260930-085958 spent 55.9k tokens and wrote nothing.
			emptied = true
			req.Think = ""
			onToken("\n\n(an empty report: asked again without thinking)\n\n")
			continue
		}
		if len(rep.Calls) == 0 && req.Role == Synthesizer && cfg.uncheckedWrite(turns) {
			// It changed something and ended without the stated check: the
			// council makes it, and the synthesizer goes on from the result.
			return "", cfg.issueCheck(key, turns, rep), nil
		}
		// Researchers only: a critic names the tools the findings used, and
		// answers from their evidence without calling any.
		if len(rep.Calls) == 0 && len(turns) == 0 && !nudged && req.Role == Researcher && cfg.narrated(req.Role, rep.Content) {
			// The narration is dropped: only the calls and their results
			// travel on, so the invention never reaches the findings.
			nudged = true
			own = append(clone(own), api.Message{Role: "assistant", Content: rep.Content}, user(narratedNudge))
			continue
		}
		verdict := firstIndex(rep.Content, []string{Retest, Done})
		if len(rep.Calls) == 0 && req.Role == Synthesizer && cfg.testing(req.Round) && unverdicted == "" && verdict < 0 && strings.TrimSpace(rep.Content) != "" {
			// C: the loop turns on the verdict; one that is missing is asked
			// for once (ab-5: a failed synthesizer answered instead, twice).
			unverdicted = rep.Content
			turns = append(slices.Clone(turns), api.Message{Role: "assistant", Content: rep.Content}, user(verdictNudge))
			onToken = func(string) {}
			continue
		}
		if len(rep.Calls) == 0 && unverdicted != "" {
			// The verdict joins the reply the user read; without one, that
			// reply stands as the answer.
			if verdict >= 0 {
				rep.Content = unverdicted + "\n\n" + strings.TrimSpace(rep.Content[verdict:])
			} else {
				rep.Content = unverdicted
			}
		}
		if gatedReply != "" {
			// The reply after the reviews a DONE waited for: back to work
			// streams again; a DONE that stands keeps the answer the user
			// has read.
			if len(rep.Calls) > 0 && unverdicted == "" {
				onToken = stream
			}
			if len(rep.Calls) == 0 && strings.Contains(rep.Content, Done) && !strings.Contains(rep.Content, Retest) {
				rep.Content = gatedReply
			}
			gatedReply = ""
		}
		if len(rep.Calls) == 0 && cfg.canReview(req.Role) && strings.Contains(rep.Content, Done) && !strings.Contains(rep.Content, Retest) && standing < maxStanding && cfg.refutedUnchanged(turns) {
			// A refutation stands until a change answers it: DONE on the
			// same result is refused, and the member goes back to work.
			standing++
			if held == "" {
				held = rep.Content
			}
			turns = append(slices.Clone(turns), api.Message{Role: "assistant", Content: rep.Content}, user(refutedNote))
			onToken = func(string) {}
			continue
		}
		if held != "" {
			if len(rep.Calls) > 0 {
				// Back to work: what it says next is streamed again.
				held, onToken = "", stream
			} else if strings.Contains(rep.Content, Done) && !strings.Contains(rep.Content, Retest) {
				rep.Content = held
			}
		}
		if len(rep.Calls) == 0 && cfg.canReview(req.Role) && cfg.testing(req.Round) && !gated && strings.Contains(rep.Content, Done) {
			// 11.9: DONE stands once the reviews still out are in, and the
			// last check was sent for one.
			gated = true
			cfg.sendLast(key, turns)
			cfg.Reviews.Wait(ctx, reviewWait)
			if rs := cfg.Reviews.Take(cfg.Turn); len(rs) > 0 {
				cfg.show(req.Round, rs)
				gatedReply = rep.Content
				turns = append(slices.Clone(turns), api.Message{Role: "assistant", Content: rep.Content}, reviewsMsg(rs), user(reviewedNudge))
				onToken = func(string) {}
				continue
			}
		}
		if len(rep.Calls) == 0 {
			if req.Role == Synthesizer {
				cfg.recordCheck(key, turns)
			}
			out := replyText(turns, rep.Content)
			// A failed check goes back to the researchers with what the
			// synthesizer's calls returned, as a finding carries its reads.
			switch {
			case typed && !writes(req.Role):
				// A typed report quotes what it proposes; the reads go by ref.
				out += cfg.evidenceRefs(req.Role, key, turns)
			case !writes(req.Role) || (req.Role == Synthesizer && cfg.retested(out, req.Round)):
				out += cfg.evidence(req.Role, key, turns)
			}
			return out, nil, nil
		}
		turns = withReasoning(turns, api.Message{Role: "assistant", Content: rep.Content, Thinking: rep.Thinking, ToolCalls: named(rep.Calls, len(turns))})
		if lim := cfg.stepLimit(req.Role); lim > 0 && toolSteps(turns) > lim {
			// Past its steps the call is not made: the member answers with
			// what it has, and a second try ends its turn as it stands.
			turns[len(turns)-1] = api.Message{Role: "assistant", Content: rep.Content}
			if noted(turns, stepsOutNote) {
				return replyText(turns, "") + cfg.evidence(req.Role, key, turns), nil, nil
			}
			turns = append(turns, user(stepsOutNote))
			onToken("\n\n(tool steps spent: asked for the report)\n\n")
			continue
		}
		cfg.post(req.Role, key, turns[len(turns)-1].ToolCalls)
		cfg.sendForReview(req.Role, key, turns)
		if len(cfg.forwarded(req.Role, key, turns)) > 0 {
			return "", turns, nil
		}
		// A turn that only read evidence back, or repeated reads the turn has
		// made, is answered here, at once.
		// A change answered in place (quote.go) costs a step, not a trip nor
		// a refusal: misquotes bounds it.
		if misq := cfg.misquotes(req.Role, key, turns); slices.ContainsFunc(turns[len(turns)-1].ToolCalls, func(c api.ToolCall) bool { _, ok := misq[c.ID]; return ok }) {
			continue
		}
		if (slices.ContainsFunc(rep.Calls, local) && (cfg.canLookup() || cfg.canPost(req.Role) || cfg.canReview(req.Role))) || slices.ContainsFunc(rep.Calls, cfg.cachedRead) {
			if lookups++; lookups > maxLookups {
				return replyText(turns, ""), nil, nil
			}
			continue
		}
		if refusals++; refusals > maxRefusals {
			return replyText(turns, ""), nil, nil
		}
	}
}

// streamPreemptible makes one model call that a mate's verdict may interrupt
// (broadcast.go). It returns the reply, or -- interrupted -- what the member
// had written so far.
func (cfg Config) streamPreemptible(ctx context.Context, tm ToolModel, req Request, key string, onToken func(string)) (Reply, string, bool, error) {
	if !cfg.canPost(req.Role) {
		rep, err := tm.StreamTools(ctx, req, onToken)
		return rep, "", false, err
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := cfg.board.listen(key, cancel)
	var partial strings.Builder
	rep, err := tm.StreamTools(cctx, req, func(s string) {
		partial.WriteString(s)
		onToken(s)
	})
	stop()
	if err != nil && ctx.Err() == nil && cfg.board.wasPreempted(key) {
		return Reply{}, partial.String(), true, nil
	}
	cfg.board.wasPreempted(key) // a verdict that came as it finished changes nothing
	return rep, "", false, err
}

// A researcher's and a critic's tool steps in one call. Past them a call is
// not made: the member is told its steps are spent, and answers with what it
// has (claude-hooks caps a critic at 3 turns, council.py:1280-1283). A step is
// a turn that called a tool the client runs (toolSteps), forwarded or answered
// from the shared reads: the critic of 20260930-085958 made 50 finds, and no
// counter saw one (claude-hooks P1). Evidence lookups cost no trip, and
// maxLookups bounds them.
const (
	researcherSteps = 4
	criticSteps     = 3
)

// stepsOutNote tells a member past its steps that the call was not made.
const stepsOutNote = "Your tool steps for this report are spent: make no more calls. Write your report now from what you have read, and name any fact you still lack instead of looking for it."

// stepLimit is how many tool steps role r takes in one call, or 0 for no
// bound here (the synthesizer's are MaxSteps, the front's its own).
func (cfg Config) stepLimit(r Role) int {
	switch r {
	case Researcher:
		return researcherSteps
	case Critic:
		return criticSteps
	}
	return 0
}

// toolSteps counts a member's turns that called a tool the client runs.
func toolSteps(turns []api.Message) int {
	n := 0
	for _, t := range turns {
		if t.Role == "assistant" && slices.ContainsFunc(t.ToolCalls, func(c api.ToolCall) bool { return !local(c) }) {
			n++
		}
	}
	return n
}

// noted reports whether the member's turns already carry note.
func noted(turns []api.Message, note string) bool {
	note = user(note).Content
	return slices.ContainsFunc(turns, func(t api.Message) bool { return t.Role == "user" && t.Content == note })
}
