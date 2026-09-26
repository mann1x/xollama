package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/xollama"
)

// remoteOllama is another server's /api/chat: it records what it was asked
// and answers every request with one streamed line.
type remoteOllama struct {
	mu   sync.Mutex
	reqs []map[string]any
}

func (r *remoteOllama) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		r.mu.Lock()
		r.reqs = append(r.reqs, m)
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"model":"remote-m","message":{"role":"assistant","content":"remote finding"},"done":true,"eval_count":7}`+"\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func remoteResearchers(host string) *xollama.Council {
	c := councilOn()
	c.Researcher = &xollama.CouncilRole{Model: "remote-m", Host: host}
	return c
}

func TestARoleOnAnAllowedHostIsServedThere(t *testing.T) {
	remote := &remoteOllama{}
	srv := remote.serve(t)
	t.Setenv("XOLLAMA_COUNCIL_HOSTS", "127.0.0.1")
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilServer(t, e, remoteResearchers(srv.URL))
	thinking, content := joined(chatChunks(t, s, api.ChatRequest{
		Model: "council", SessionID: "conv-1",
		Messages: []api.Message{{Role: "user", Content: "Why is the sky blue?"}},
	}))
	if content == "" || !strings.Contains(thinking, "remote finding") {
		t.Errorf("the remote researchers' findings are not in the deliberation: %q", thinking)
	}
	if len(remote.reqs) != 2 {
		t.Fatalf("the remote served %d requests, want the 2 researchers", len(remote.reqs))
	}
	for _, r := range remote.reqs {
		if r["model"] != "remote-m" {
			t.Errorf("asked the remote for %v, want remote-m", r["model"])
		}
		if _, ok := r["session_id"]; ok {
			t.Errorf("sent this server's session to another: %v", r["session_id"])
		}
	}
	if slices.Contains(e.roles, "researcher") {
		t.Errorf("a researcher ran here too: %v", e.roles)
	}
}

// A host the operator has not allowed is never called; its researchers fall
// back to the council's own model.
func TestAHostTheOperatorHasNotAllowedIsNeverCalled(t *testing.T) {
	remote := &remoteOllama{}
	srv := remote.serve(t)
	t.Setenv("XOLLAMA_COUNCIL_HOSTS", "")
	e := &councilEngine{route: `{"route":"council"}`}
	s := councilServer(t, e, remoteResearchers(srv.URL))
	thinking, content := joined(chatChunks(t, s, api.ChatRequest{
		Model: "council", Messages: []api.Message{{Role: "user", Content: "Why is the sky blue?"}},
	}))
	if len(remote.reqs) != 0 {
		t.Errorf("called a host that is not allowed %d times", len(remote.reqs))
	}
	if content == "" || !strings.Contains(thinking, "the council's model answers instead") {
		t.Errorf("no fallback in the deliberation: %q", thinking)
	}
	n := 0
	for _, r := range e.roles {
		if r == "researcher" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d researchers ran on the council's model, want 2 (roles %v)", n, e.roles)
	}
}

func TestCouncilHostAllowed(t *testing.T) {
	for _, tc := range []struct {
		host, allowed string
		ok            bool
	}{
		{"http://gpu2:11434", "gpu2", true},
		{"http://gpu2:11434", "GPU2:11434", true},
		{"http://gpu2:11434", "gpu2:22434", false},
		{"http://gpu2:11434", "gpu3, gpu4", false},
		{"http://gpu2:11434", "*", true},
		{"http://gpu2:11434", "", false},
		{"not a url", "*", false},
	} {
		if _, err := councilHostAllowed(tc.host, tc.allowed); (err == nil) != tc.ok {
			t.Errorf("councilHostAllowed(%q, %q) = %v, want ok %v", tc.host, tc.allowed, err, tc.ok)
		}
	}
}
