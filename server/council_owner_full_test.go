package server

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/internal/council"
	"github.com/ollama/ollama/llm"
)

// ownerFullKV refuses the synthesizer's first request as the engine does a
// member whose owner is full, and records whether it was marked to be.
type ownerFullKV struct {
	*fakeKV
	refused, marked atomic.Int32
}

func (f *ownerFullKV) Completion(ctx context.Context, req llm.CompletionRequest, fn func(llm.CompletionResponse)) error {
	if strings.Contains(req.Prompt, "ROLE: SYNTHESIZER") && f.refused.Add(1) == 1 {
		if llm.CompactsOnFull(ctx) {
			f.marked.Add(1)
		}
		return api.StatusError{StatusCode: http.StatusInsufficientStorage, ErrorMessage: llm.ErrOwnerFull.Error() + ": 37121 of 196608 cells free, needs 39662"}
	}
	return f.fakeKV.Completion(ctx, req, fn)
}

// A member refused for a full owner, with nothing in flight to give cells
// back, folds the conversation: the turn lets its pools go, compacts,
// rebuilds its root from the fold, and resumes from the members that settled
// -- the planner and researchers are not asked again. On eleven2go (a0968aea,
// council run 4) the refusal was waited out for two minutes and the turn
// failed.
func TestAFullOwnerCompactsAndTheTurnResumes(t *testing.T) {
	councilRoots.reset()
	councilCompactions.reset()
	councilStashes.reset()
	e := &councilEngine{route: `{"route":"council"}`}
	kv := &ownerFullKV{fakeKV: &fakeKV{grant: 16384, used: 900, session: "conv-1"}}
	kv.councilRunner = &councilRunner{mockRunner: &mockRunner{contextLength: 32768}, e: e}
	s := councilServerOn(t, kv, councilOn(), nil)
	_, content := joined(chatChunks(t, s, longCouncilReq("conv-1", "Why is the sky blue?")))
	councilIdle.Wait()
	if !strings.HasPrefix(content, "The sky is blue") {
		t.Fatalf("answer %q: the turn must go on after the fold", content)
	}
	if kv.refused.Load() < 2 || kv.marked.Load() != 1 {
		t.Fatalf("synthesizer asked %d times, marked %d: want refused once when marked, then answered", kv.refused.Load(), kv.marked.Load())
	}
	if calls, compacted := e.summaries(); calls == 0 || !compacted {
		t.Fatalf("%d writer calls, compacted %v: want the conversation folded", calls, compacted)
	}
	if n := e.count("planner"); n != 1 {
		t.Errorf("planner asked %d times: the resumed turn must not plan again", n)
	}
	if n := e.count("researcher"); n != 2 {
		t.Errorf("researchers asked %d times: settled members are not asked again", n)
	}
	// Every pool built before the fold was let go before the root was rebuilt.
	kv.mu.Lock()
	defer kv.mu.Unlock()
	var roots []int
	for _, p := range kv.pools {
		if p.parent == nil {
			roots = append(roots, p.id)
		}
	}
	if len(roots) < 2 || !slices.Contains(kv.released, roots[0]) {
		t.Errorf("roots %v, released %v: want the old root let go and a new one built", roots, kv.released)
	}
}

// A member refused while another owner-bound member runs waits for it to
// finish and asks again, instead of folding the turn; with none in flight its
// refusal stands at once.
func TestAFullOwnerWaitsForAMemberThatGivesCellsBack(t *testing.T) {
	full := ownerFullError{"council synthesizer: " + llm.ErrOwnerFull.Error()}
	cm := &councilMembers{tree: &councilTree{owner: "conv-1"}}
	cm.tree.takeoff()
	var calls atomic.Int32
	go func() {
		time.Sleep(20 * time.Millisecond)
		cm.tree.land()
	}()
	// Each call takes off and lands as a real owner-bound member does: its
	// own landing is not another member giving cells back.
	_, _, _, err := cm.retryOwnerFull(t.Context(), council.Request{Role: council.Synthesizer}, func() (string, []api.ToolCall, bool, error) {
		cm.tree.takeoff()
		defer cm.tree.land()
		if calls.Add(1) == 1 {
			return "", nil, false, full
		}
		return "ok", nil, false, nil
	})
	if err != nil || calls.Load() != 2 {
		t.Fatalf("err %v after %d calls: want the second to answer", err, calls.Load())
	}

	calls.Store(0)
	_, _, _, err = cm.retryOwnerFull(t.Context(), council.Request{Role: council.Synthesizer}, func() (string, []api.ToolCall, bool, error) {
		cm.tree.takeoff()
		defer cm.tree.land()
		calls.Add(1)
		return "", nil, false, full
	})
	if !errors.Is(err, llm.ErrOwnerFull) || calls.Load() != 1 {
		t.Fatalf("err %v after %d calls: want the refusal at once, nothing in flight", err, calls.Load())
	}
}

// Only a member that holds the owner's cells is marked: a reviewer or the
// builder on a session of its own, a compaction call, and an unowned tree
// wait out admission as before.
func TestOnlyOwnerBoundMembersAreRefusedAtOnce(t *testing.T) {
	cm := &councilMembers{tree: &councilTree{owner: "conv-1"}}
	for _, tc := range []struct {
		role            council.Role
		session, worker string
		want            bool
	}{
		{council.Researcher, "conv-1~researcher-1", "conv-1~researcher-1", true},
		{council.Planner, "conv-1", "", true},
		{council.Reviewer, "conv-1~reviewer-1", "", false},
		{roleCompactWriter, "conv-1", "", false},
	} {
		if got := cm.ownerBound(council.Request{Role: tc.role}, tc.session, tc.worker); got != tc.want {
			t.Errorf("%s on %q: owner-bound %v, want %v", tc.role, tc.session, got, tc.want)
		}
	}
	cm.tree.unowned = true
	if cm.ownerBound(council.Request{Role: council.Researcher}, "conv-1~researcher-1", "conv-1~researcher-1") {
		t.Error("an unowned tree has no owner to be full")
	}
}
