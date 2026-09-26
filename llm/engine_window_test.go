package llm

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/ollama/ollama/types/xollama"
)

// num_ctx 0 means the whole pool only where there are pools, and never on a
// model pinned to stock llama.cpp, where it keeps upstream's meaning.
func TestWantsWholePool(t *testing.T) {
	t.Setenv("XOLLAMA_SESSION_POOL", "false")
	t.Setenv("XOLLAMA_ENGINE", "auto")
	for _, tc := range []struct {
		name string
		cfg  LlamaServerConfig
		want bool
	}{
		{"no pools", LlamaServerConfig{}, false},
		{"council pools", LlamaServerConfig{CouncilPools: 4}, true},
		{"pinned to llama.cpp", LlamaServerConfig{CouncilPools: 4, Xollama: &xollama.Config{Version: 4, Engine: xollama.EngineLlamaCpp}}, false},
	} {
		if got := WantsWholePool(tc.cfg); got != tc.want {
			t.Errorf("%s: WantsWholePool = %v, want %v", tc.name, got, tc.want)
		}
	}
	t.Setenv("XOLLAMA_ENGINE", "llamacpp")
	if WantsWholePool(LlamaServerConfig{CouncilPools: 4}) {
		t.Error("XOLLAMA_ENGINE=llamacpp must keep upstream's num_ctx 0")
	}
}

func TestResolveWholePool(t *testing.T) {
	for _, tc := range []struct {
		numCtx, train int
		polykv        bool
		want          int
	}{
		{8192, 131072, true, 8192},              // a stated window is left alone
		{NumCtxWholePool, 131072, true, 131072}, // the whole trained context
		{NumCtxWholePool, 131072, false, 4},     // no PolyKV: upstream's minimum
		{NumCtxWholePool, 0, true, 4},           // an unknown trained context
	} {
		if got := ResolveWholePool(tc.numCtx, tc.train, tc.polykv); got != tc.want {
			t.Errorf("ResolveWholePool(%d, %d, %v) = %d, want %d", tc.numCtx, tc.train, tc.polykv, got, tc.want)
		}
	}
}

// A council pool with no session is created unowned, never as session "".
func TestACouncilPoolWithoutASessionIsUnowned(t *testing.T) {
	for _, tc := range []struct {
		session string
		unowned bool
	}{{"", true}, {"conv-1", false}} {
		stub := &poolStub{payload: `{"pool_id":0}`, tokenPayload: `{"tokens":[1,2]}`}
		s := poolRunner(t, stub)
		if _, err := s.CreatePool(t.Context(), tc.session, nil, "prefix"); err != nil {
			t.Fatal(err)
		}
		var sent map[string]any
		if err := json.Unmarshal([]byte(stub.bodies[len(stub.bodies)-1]), &sent); err != nil {
			t.Fatal(err)
		}
		_, hasSession := sent["session_id"]
		if (sent["unowned"] == true) != tc.unowned || hasSession == tc.unowned {
			t.Errorf("session %q sent %v", tc.session, sent)
		}
	}
}

// The engine's "compact the session" is ErrSessionFull, so the council can
// compact; any other refusal is not.
func TestAFullSessionRefusalIsErrSessionFull(t *testing.T) {
	for _, tc := range []struct {
		payload string
		full    bool
	}{
		{`{"error":{"code":503,"message":"session allocation full ('conv-1': 12 of 16384 cells free, the pool needs 9000) — compact the session"}}`, true},
		{`{"error":{"code":503,"message":"pool seq-id reservoir exhausted (--polykv-pool-seqs); release a pool first"}}`, false},
	} {
		stub := &poolStub{status: http.StatusServiceUnavailable, payload: tc.payload, tokenPayload: `{"tokens":[1,2]}`}
		s := poolRunner(t, stub)
		_, err := s.CreatePool(t.Context(), "conv-1", nil, "prefix")
		if err == nil || errors.Is(err, ErrSessionFull) != tc.full {
			t.Errorf("%s: err %v, want ErrSessionFull %v", tc.payload, err, tc.full)
		}
	}
}
