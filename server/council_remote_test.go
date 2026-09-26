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
	"github.com/ollama/ollama/internal/council"
	"github.com/ollama/ollama/types/xollama"
)

// remoteOllama is another server's /api/chat: it records what it was asked
// and answers every request with one streamed line.
type remoteOllama struct {
	mu   sync.Mutex
	reqs []map[string]any
	// xollama answers the fork's identity route; cloud says, in /api/show,
	// that the model is served from ollama.com.
	xollama, cloud bool
}

func (r *remoteOllama) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/xollama":
			if !r.xollama {
				http.NotFound(w, req)
				return
			}
			_, _ = io.WriteString(w, `{"xollama":true}`)
			return
		case "/api/version":
			_, _ = io.WriteString(w, `{"version":"0.34.2"}`)
			return
		case "/api/show":
			if r.cloud {
				_, _ = io.WriteString(w, `{"remote_host":"https://ollama.com:443","remote_model":"remote-m"}`)
			} else {
				_, _ = io.WriteString(w, `{}`)
			}
			return
		}
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

// A token budget goes only where it is understood: this server's own models,
// and a model another xollama serves itself. A cloud model and a stock ollama
// get think true, with the budget as room in num_predict.
func TestAThinkingMemberGetsABudgetOnlyWhereOneIsUnderstood(t *testing.T) {
	budget := float64(xollama.DefaultCouncilThinkBudget)
	for _, tc := range []struct {
		name           string
		xollama, cloud bool
		want           any
	}{
		{"stock ollama", false, false, true},
		{"xollama, its own model", true, false, budget},
		{"xollama, a cloud model", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			councilProbes.reset()
			remote := &remoteOllama{xollama: tc.xollama, cloud: tc.cloud}
			srv := remote.serve(t)
			t.Setenv("XOLLAMA_COUNCIL_HOSTS", "127.0.0.1")
			c := remoteResearchers(srv.URL)
			c.Researcher.Think = "on"
			s := councilServer(t, &councilEngine{route: `{"route":"council"}`}, c)
			chatChunks(t, s, api.ChatRequest{Model: "council", Messages: []api.Message{{Role: "user", Content: "Why is the sky blue?"}}})
			if len(remote.reqs) != 2 {
				t.Fatalf("the remote served %d chats, want 2", len(remote.reqs))
			}
			for _, r := range remote.reqs {
				opts, _ := r["options"].(map[string]any)
				if r["think"] != tc.want || opts["num_predict"] != float64(384)+budget {
					t.Errorf("think %v, num_predict %v; want %v and %v", r["think"], opts["num_predict"], tc.want, float64(384)+budget)
				}
			}
		})
	}
}

func TestACloudModelHereTakesNoBudget(t *testing.T) {
	defer func(f func(string) bool) { councilModelIsCloud = f }(councilModelIsCloud)
	councilModelIsCloud = func(name string) bool { return name == "glm-5.3-flash-tpl2:latest" }
	cm := &councilMembers{}
	for _, tc := range []struct {
		r    council.Request
		want bool
	}{
		{council.Request{}, true},
		{council.Request{Model: "qwen3:4b"}, true},
		// A pulled cloud tag need not say so in its name.
		{council.Request{Model: "glm-5.3-flash-tpl2:latest"}, false},
	} {
		if got := cm.councilTakesBudget(t.Context(), tc.r); got != tc.want {
			t.Errorf("%q: takes a budget %v, want %v", tc.r.Model, got, tc.want)
		}
	}
}

func TestACloudReferenceIsCloudByName(t *testing.T) {
	if !councilModelIsCloud("gemma4:31b-cloud") || !councilModelIsCloud("gpt-oss:120b:cloud") {
		t.Error("a cloud reference was not recognised")
	}
}

// A pulled cloud tag is cloud by its manifest, whatever its name says.
func TestAPulledCloudTagIsCloudByItsManifest(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", t.TempDir())
	var s Server
	stream := false
	w := createRequest(t, s.CreateHandler, api.CreateRequest{
		Model: "glm-5.3-flash-tpl2", From: "glm-5.3-flash", RemoteHost: "https://ollama.com", Stream: &stream,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if !councilModelIsCloud("glm-5.3-flash-tpl2") {
		t.Error("a tag whose manifest names a remote host is not cloud")
	}
	if councilModelIsCloud("no-such-model") {
		t.Error("a model this server does not have is cloud")
	}
}
