package server

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/internal/council"
	"github.com/ollama/ollama/ml"
)

// councilStateKeyIn points the sealing key at dir for one test.
func councilStateKeyIn(t *testing.T, dir string) {
	t.Helper()
	old := councilStateKeyPath
	councilStateKeyPath = func() string { return filepath.Join(dir, councilStateKeyFile) }
	councilStateKey.once, councilStateKey.aead, councilStateKey.err = sync.Once{}, nil, nil
	t.Cleanup(func() {
		councilStateKeyPath = old
		councilStateKey.once, councilStateKey.aead, councilStateKey.err = sync.Once{}, nil, nil
	})
}

func testState() councilState {
	return councilState{
		history: []byte("h"), turn: []byte("t"),
		progress: council.Progress{
			Route: "council", Plan: &council.Plan{Plan: "p", Briefs: []string{"a", "b"}},
			Rounds: []council.RoundProgress{{Findings: []string{"f1", ""}, Critiques: []string{"", ""}}},
		},
		record: &compactionRecord{
			n: 4, hash: "abc", gen: 2, head: []api.Message{{Role: "user", Content: "summary"}},
			requests: []string{"q1", "q2"}, retro: "r", replay: "rp", how: "pooled",
		},
	}
}

func TestACouncilStateSealsAndOpens(t *testing.T) {
	councilStateKeyIn(t, t.TempDir())
	blob, err := sealCouncilState(testState())
	if err != nil {
		t.Fatal(err)
	}
	got, err := openCouncilState(blob)
	if err != nil {
		t.Fatal(err)
	}
	want := testState()
	if string(got.history) != "h" || string(got.turn) != "t" || got.progress.Route != "council" ||
		got.progress.Plan == nil || got.progress.Plan.Briefs[1] != "b" ||
		len(got.progress.Rounds) != 1 || len(got.progress.Rounds[0].Findings) != 2 || got.progress.Rounds[0].Findings[1] != "" ||
		len(got.progress.Rounds[0].Critiques) != 2 {
		t.Errorf("progress %+v", got.progress)
	}
	r := got.record
	if r == nil || r.n != want.record.n || r.hash != "abc" || r.gen != 2 || len(r.head) != 1 || r.head[0].Content != "summary" ||
		len(r.requests) != 2 || r.retro != "r" || r.replay != "rp" || r.how != "pooled" {
		t.Errorf("record %+v", r)
	}

	// Any changed byte is no state.
	raw, _ := base64.StdEncoding.DecodeString(blob)
	raw[len(raw)-1] ^= 1
	if _, err := openCouncilState(base64.StdEncoding.EncodeToString(raw)); err == nil {
		t.Error("a tampered state opened")
	}
	if _, err := openCouncilState("not base64!"); err == nil {
		t.Error("garbage opened")
	}
	// Another server's key cannot open it.
	councilStateKeyIn(t, t.TempDir())
	if _, err := openCouncilState(blob); err == nil {
		t.Error("a state opened under another key")
	}
}

// Fields a newer server adds are skipped, as protobuf requires; an unknown
// version is refused.
func TestACouncilStateSkipsWhatItDoesNotKnow(t *testing.T) {
	b := testState().marshal()
	b = protowire.AppendTag(b, 99, protowire.BytesType)
	b = protowire.AppendString(b, "from the future")
	if s, err := unmarshalCouncilState(b); err != nil || s.progress.Route != "council" {
		t.Errorf("with an unknown field: %v %+v", err, s.progress)
	}
	old := protowire.AppendVarint(protowire.AppendTag(nil, 1, protowire.VarintType), 7)
	if _, err := unmarshalCouncilState(old); err == nil {
		t.Error("an unknown version opened")
	}
}

// The notes board (10.6) travels in the state, so a resumed turn neither
// delivers a note twice nor loses one a mate has not read yet.
func TestTheNotesBoardTravelsInTheState(t *testing.T) {
	s := testState()
	s.progress.Notes = []council.Note{{ID: "n1", From: "researcher 1", Text: "I take the parser"}, {ID: "n2", From: "researcher 2", Text: "tests pass"}}
	s.progress.Seen = map[string]int{"research/0/1": 2, "research/0/0": 0}
	got, err := unmarshalCouncilState(s.marshal())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.progress.Notes, s.progress.Notes) {
		t.Errorf("notes = %+v, want %+v", got.progress.Notes, s.progress.Notes)
	}
	if !reflect.DeepEqual(got.progress.Seen, s.progress.Seen) {
		t.Errorf("seen = %+v, want %+v", got.progress.Seen, s.progress.Seen)
	}
}

// The failed checks of a turn (11.4) travel in the state, in order, so a
// resumed turn starts at the cycle after the last.
func TestTheFailedChecksTravelInTheState(t *testing.T) {
	s := testState()
	s.progress.Tests = []string{"tried a; the test failed. " + council.Retest, "tried b; still failing. " + council.Retest}
	s.progress.Replans = []council.Plan{{Plan: "p2", Briefs: []string{"x", "y"}}, {Plan: "p3", Briefs: []string{"z", "w"}}}
	got, err := unmarshalCouncilState(s.marshal())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.progress.Tests, s.progress.Tests) {
		t.Errorf("tests = %q, want %q", got.progress.Tests, s.progress.Tests)
	}
	if !reflect.DeepEqual(got.progress.Replans, s.progress.Replans) {
		t.Errorf("replans = %+v, want %+v", got.progress.Replans, s.progress.Replans)
	}
}

// The key is kept in the store, owner-only, and read back after a restart.
func TestTheCouncilStateKeyIsKept(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store", councilStateKeyFile)
	k1, err := loadCouncilStateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file %v %v", fi, err)
	}
	k2, err := loadCouncilStateKey(path)
	if err != nil || string(k1) != string(k2) {
		t.Errorf("the key changed on a reread: %v", err)
	}
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCouncilStateKey(path); err == nil {
		t.Error("a malformed key was used")
	}
}

func stateChunks(chunks []api.ChatResponse) (checkpoints []string, done string) {
	for _, c := range chunks {
		switch {
		case c.Done:
			done = c.CouncilChatState
		case c.CouncilChatState != "":
			checkpoints = append(checkpoints, c.CouncilChatState)
		}
	}
	return checkpoints, done
}

// A turn that asks for state gets a sealed checkpoint as each member
// finishes, on a chunk of its own, and one on the done chunk; a turn that
// does not ask gets none.
func TestACouncilTurnSendsItsState(t *testing.T) {
	councilStateKeyIn(t, t.TempDir())
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilServer(t, e, councilOn())
	empty := ""
	req := api.ChatRequest{Model: "council", CouncilChatState: &empty, Messages: []api.Message{{Role: "user", Content: "Why is the sky blue?"}}}
	chunks := chatChunks(t, s, req)
	checkpoints, done := stateChunks(chunks)
	if len(checkpoints) != 6 || done == "" {
		t.Fatalf("%d checkpoints, done state %v; want 6 and one", len(checkpoints), done != "")
	}
	for _, c := range chunks {
		if c.CouncilChatState != "" && !c.Done && (c.Message.Content != "" || c.Message.Thinking != "" || c.Council != nil) {
			t.Errorf("a state chunk carries more: %+v", c)
		}
	}
	if st, err := openCouncilState(done); err != nil || st.progress.Route != "" {
		t.Errorf("the done state %v %+v: want no progress left", err, st.progress)
	}

	req.CouncilChatState = nil
	if cp, d := stateChunks(chatChunks(t, s, req)); len(cp) != 0 || d != "" {
		t.Errorf("a turn that did not ask got %d states", len(cp))
	}
}

// A state from a turn that broke off resumes it: the members it records as
// done are not asked again. The same state with another user message starts
// the turn afresh.
func TestABrokenOffTurnResumes(t *testing.T) {
	councilStateKeyIn(t, t.TempDir())
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilServer(t, e, councilOn())
	empty := ""
	req := api.ChatRequest{Model: "council", CouncilChatState: &empty, Messages: []api.Message{{Role: "user", Content: "Why is the sky blue?"}}}
	checkpoints, _ := stateChunks(chatChunks(t, s, req))
	if len(checkpoints) != 6 {
		t.Fatalf("%d checkpoints", len(checkpoints))
	}
	// After the route, the plan and both researchers.
	blob := checkpoints[3]
	st, err := openCouncilState(blob)
	if err != nil || st.progress.Plan == nil || st.progress.Rounds[0].Findings[0] == "" || st.progress.Rounds[0].Findings[1] == "" {
		t.Fatalf("checkpoint 4: %v %+v", err, st.progress)
	}

	e2 := &councilEngine{route: `{"route":"council"}`}
	s2 := councilServer(t, e2, councilOn())
	req.CouncilChatState = &blob
	_, content := joined(chatChunks(t, s2, req))
	if !strings.HasPrefix(content, "The sky is blue") {
		t.Fatalf("resumed answer %q", content)
	}
	for role, n := range map[string]int{"route": 0, "planner": 0, "researcher": 0, "critic": 2, "synthesizer": 1} {
		if got := e2.count(role); got != n {
			t.Errorf("resumed: %s asked %d times, want %d", role, got, n)
		}
	}

	e3 := &councilEngine{route: `{"route":"council"}`}
	s3 := councilServer(t, e3, councilOn())
	other := req
	other.Messages = []api.Message{{Role: "user", Content: "Why is the sunset red?"}}
	chatChunks(t, s3, other)
	if e3.count("planner") != 1 || e3.count("researcher") != 2 {
		t.Errorf("another question resumed a state not made for it: planner %d researchers %d", e3.count("planner"), e3.count("researcher"))
	}
}

// The done state carries the compaction record; a server that lost it --
// restarted -- takes it back from the client and does not fold again.
func TestTheStateRestoresALostRecord(t *testing.T) {
	councilStateKeyIn(t, t.TempDir())
	councilCompactions.reset()
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilServer(t, e, councilOn())
	req := stockLongReq("conv-restore")
	empty := ""
	req.CouncilChatState = &empty
	_, done := stateChunks(chatChunks(t, s, req))
	councilIdle.Wait()
	rec := councilCompactions.get("conv-restore")
	if rec == nil || done == "" {
		t.Fatalf("record %v, done state %v", rec != nil, done != "")
	}
	st, err := openCouncilState(done)
	if err != nil || st.record == nil || st.record.hash != rec.hash {
		t.Fatalf("the done state carries %+v (%v), want the record", st.record, err)
	}

	councilCompactions.reset() // the restart
	next := nextTurn(req, "Thanks.")
	next.CouncilChatState = &done
	e.mu.Lock()
	writers := 0
	for _, r := range e.roles {
		if r == "compaction-writer" || r == "compaction-text-writer" {
			writers++
		}
	}
	e.mu.Unlock()
	chatChunks(t, s, next)
	councilIdle.Wait()
	if got := councilCompactions.get("conv-restore"); got == nil || got.gen < rec.gen {
		t.Fatalf("record after the restart %+v", got)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	after := 0
	for _, r := range e.roles {
		if r == "compaction-writer" || r == "compaction-text-writer" {
			after++
		}
	}
	if after != writers {
		t.Errorf("the conversation was folded again (%d writer calls, had %d)", after, writers)
	}
}

// A client that leaves mid-turn ends the turn. The scheduler drops a request
// whose context has ended without answering it, so a member being scheduled
// then never hears back; the turn must not wait on it, or it holds its root
// and the next turn on the conversation (the one resuming) waits forever.
func TestALeftTurnEndsWhileAMemberIsUnanswered(t *testing.T) {
	councilStateKeyIn(t, t.TempDir())
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilServer(t, e, councilOn())
	ctx, leave := context.WithCancel(t.Context())
	defer leave()
	answer := s.sched.loadFn
	var loads atomic.Int32
	s.sched.loadFn = func(req *LlmRequest, si ml.SystemInfo, gpus []ml.DeviceInfo, requireFull bool) bool {
		// The route, the plan and both researchers; then the client leaves
		// and the first critic is dropped, as processPending drops it.
		if loads.Add(1) <= 4 {
			return answer(req, si, gpus, requireFull)
		}
		leave()
		return false
	}

	empty := ""
	body, err := json.Marshal(api.ChatRequest{Model: "council", CouncilChatState: &empty, Messages: []api.Message{{Role: "user", Content: "Why is the sky blue?"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLLAMA_MODELS", cmp.Or(os.Getenv("OLLAMA_MODELS"), t.TempDir()))
	c, _ := gin.CreateTestContext(NewRecorder())
	c.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/chat", bytes.NewReader(body))
	done := make(chan struct{})
	go func() {
		s.ChatHandler(c)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the turn is still waiting on a member the scheduler dropped")
	}
}

// A suspended member's turns travel in the state exactly, tool arguments in
// their order included (9.5).
func TestASuspendedMemberTravelsInTheState(t *testing.T) {
	councilStateKeyIn(t, t.TempDir())
	args := api.NewToolCallFunctionArguments()
	args.Set("path", "notes.txt")
	args.Set("lines", float64(3))
	st := testState()
	st.progress.Suspended = map[string][]api.Message{
		"r2": {{Role: "assistant", Content: "let me look", ToolCalls: []api.ToolCall{{ID: "call_1", Function: api.ToolCallFunction{Name: "read_files", Arguments: args}}}}},
		"s":  {{Role: "assistant", ToolCalls: []api.ToolCall{{ID: "call_2", Function: api.ToolCallFunction{Name: "write_file"}}}}},
	}
	blob, err := sealCouncilState(st)
	if err != nil {
		t.Fatal(err)
	}
	got, err := openCouncilState(blob)
	if err != nil {
		t.Fatal(err)
	}
	r2 := got.progress.Suspended["r2"]
	if len(got.progress.Suspended) != 2 || len(r2) != 1 || r2[0].Content != "let me look" || r2[0].ToolCalls[0].ID != "call_1" {
		t.Fatalf("suspended %+v", got.progress.Suspended)
	}
	if a := r2[0].ToolCalls[0].Function.Arguments.String(); a != `{"path":"notes.txt","lines":3}` {
		t.Fatalf("arguments %s", a)
	}
}
