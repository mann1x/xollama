package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/sync/semaphore"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/xollama"
)

func boolPtr(b bool) *bool { return &b }

func intPtr(i int) *int { return &i }

func cfgWithSession(s *xollama.Session) LlamaServerConfig {
	return LlamaServerConfig{Xollama: &xollama.Config{Version: 1, Session: s}}
}

// A session identity has to survive the conversation growing. If it changed on
// every turn the engine would be asked to pin a different slot each time, which
// is worse than asking for nothing.
func TestDeriveSessionIDIsStableAsTheConversationGrows(t *testing.T) {
	tools := api.Tools{{Function: api.ToolFunction{Name: "read_file"}}}
	turn1 := []api.Message{
		{Role: "system", Content: "You are a careful assistant."},
		{Role: "user", Content: "Why is the sky blue?"},
	}
	turn3 := append(append([]api.Message{}, turn1...),
		api.Message{Role: "assistant", Content: "Rayleigh scattering."},
		api.Message{Role: "user", Content: "And at sunset?"},
	)

	first := DeriveSessionID("digest", turn1, tools)
	if first == "" {
		t.Fatal("expected an identity for a conversation with a system prompt and a user turn")
	}
	if !strings.HasPrefix(first, sessionIDPrefix) {
		t.Errorf("derived id %q should be marked as ours with prefix %q", first, sessionIDPrefix)
	}
	if got := DeriveSessionID("digest", turn3, tools); got != first {
		t.Errorf("identity changed as the conversation grew: %q then %q", first, got)
	}
}

func TestDeriveSessionIDSeparatesConversations(t *testing.T) {
	sys := api.Message{Role: "system", Content: "You are a careful assistant."}
	a := DeriveSessionID("digest", []api.Message{sys, {Role: "user", Content: "question one"}}, nil)
	b := DeriveSessionID("digest", []api.Message{sys, {Role: "user", Content: "question two"}}, nil)
	if a == b {
		t.Error("two conversations sharing only their system prompt must not share an identity")
	}

	// Same conversation, different model: different runner, and mixing them in
	// one identity would be meaningless.
	c := DeriveSessionID("other-digest", []api.Message{sys, {Role: "user", Content: "question one"}}, nil)
	if a == c {
		t.Error("the model must take part in the identity")
	}

	// Tools are part of the prefix the KV holds, so they are part of what
	// makes a conversation itself.
	d := DeriveSessionID("digest", []api.Message{sys, {Role: "user", Content: "question one"}},
		api.Tools{{Function: api.ToolFunction{Name: "read_file"}}})
	if a == d {
		t.Error("tool definitions must take part in the identity")
	}
}

// Nothing to pin is not a conversation. Returning an id here would give every
// bare prompt its own slot reservation for no benefit.
func TestDeriveSessionIDEmptyWithoutAConversation(t *testing.T) {
	for _, tt := range []struct {
		name string
		msgs []api.Message
	}{
		{"no messages at all", nil},
		{"only an assistant turn", []api.Message{{Role: "assistant", Content: "hello"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveSessionID("digest", tt.msgs, nil); got != "" {
				t.Errorf("expected no identity, got %q", got)
			}
		})
	}
}

func TestSessionFieldsPrecedence(t *testing.T) {
	tests := []struct {
		name     string
		opencoti bool
		envAff   string
		envPool  string
		cfg      LlamaServerConfig
		id       string
		pool     *int
		wantID   string
		wantPool *int
		wantWhy  string
	}{
		{
			name: "stock llama.cpp carries nothing, whatever anyone asked for",
			// The off path has to stay byte-identical to upstream, so this is
			// the one rule that outranks every other setting here.
			opencoti: false, envAff: "1", envPool: "1",
			cfg: cfgWithSession(&xollama.Session{Affinity: boolPtr(true), Pool: boolPtr(true)}),
			id:  "xo-abc", pool: intPtr(3),
			wantID: "", wantPool: nil,
			wantWhy: "engine gate",
		},
		{
			name:     "affinity is on by default",
			opencoti: true,
			id:       "xo-abc",
			wantID:   "xo-abc",
			wantWhy:  "default",
		},
		{
			name:     "the environment can switch affinity off",
			opencoti: true, envAff: "0",
			id:      "xo-abc",
			wantID:  "",
			wantWhy: "env",
		},
		{
			name:     "the model overrides the environment, both ways",
			opencoti: true, envAff: "0",
			cfg:     cfgWithSession(&xollama.Session{Affinity: boolPtr(true)}),
			id:      "xo-abc",
			wantID:  "xo-abc",
			wantWhy: "model config wins",
		},
		{
			name: "a model that switches affinity off drops a caller's own id",
			// Off is an operator or a publisher saying this model must not pin
			// conversations; a client field does not overrule that.
			opencoti: true, envAff: "1",
			cfg:     cfgWithSession(&xollama.Session{Affinity: boolPtr(false)}),
			id:      "caller-supplied",
			wantID:  "",
			wantWhy: "off means off",
		},
		{
			name:     "a pool is dropped unless pooling was asked for",
			opencoti: true,
			id:       "xo-abc", pool: intPtr(7),
			wantID: "xo-abc", wantPool: nil,
			wantWhy: "pool defaults off",
		},
		{
			name:     "pooling on carries the pool through",
			opencoti: true,
			cfg:      cfgWithSession(&xollama.Session{Pool: boolPtr(true)}),
			id:       "xo-abc", pool: intPtr(7),
			wantID: "xo-abc", wantPool: intPtr(7),
			wantWhy: "model asked for a pool",
		},
		{
			name: "pooling cannot survive affinity being off",
			// A shared prefix pool has nothing to attach to without session
			// identity. The config validator refuses this pair, but the
			// environment can still be asked for it.
			opencoti: true, envAff: "0", envPool: "1",
			id: "xo-abc", pool: intPtr(7),
			wantID: "", wantPool: nil,
			wantWhy: "pool needs affinity",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envAff != "" {
				t.Setenv("XOLLAMA_SESSION_AFFINITY", tt.envAff)
			}
			if tt.envPool != "" {
				t.Setenv("XOLLAMA_SESSION_POOL", tt.envPool)
			}
			gotID, gotPool := sessionFieldsFor(tt.opencoti, tt.cfg, tt.id, tt.pool)
			if gotID != tt.wantID {
				t.Errorf("session id = %q, want %q (%s)", gotID, tt.wantID, tt.wantWhy)
			}
			switch {
			case gotPool == nil && tt.wantPool == nil:
			case gotPool == nil || tt.wantPool == nil:
				t.Errorf("pool id = %v, want %v (%s)", fmtPool(gotPool), fmtPool(tt.wantPool), tt.wantWhy)
			case *gotPool != *tt.wantPool:
				t.Errorf("pool id = %d, want %d (%s)", *gotPool, *tt.wantPool, tt.wantWhy)
			}
		})
	}
}

// The fields have to actually reach llama-server's completion body on the
// engine that understands them, and be absent from it everywhere else. This
// asserts the wire, because that is the only thing the engine sees.
func TestLlamaServerCompletionSessionFields(t *testing.T) {
	for _, tt := range []struct {
		name     string
		opencoti bool
		cfg      LlamaServerConfig
		req      CompletionRequest
		wantID   any
		wantPool any
	}{
		{
			name:     "opencoti carries the session id",
			opencoti: true,
			req:      CompletionRequest{Prompt: "hi", SessionID: "xo-1234"},
			wantID:   "xo-1234",
		},
		{
			name:     "opencoti carries a pool when the model asked for one",
			opencoti: true,
			cfg:      cfgWithSession(&xollama.Session{Pool: boolPtr(true)}),
			req:      CompletionRequest{Prompt: "hi", SessionID: "xo-1234", PoolID: intPtr(2)},
			wantID:   "xo-1234",
			wantPool: float64(2),
		},
		{
			name:     "stock llama.cpp carries neither",
			opencoti: false,
			req:      CompletionRequest{Prompt: "hi", SessionID: "xo-1234", PoolID: intPtr(2)},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var body map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/health":
					fmt.Fprint(w, `{"status":"ok"}`)
				case "/completion":
					raw, err := io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("reading completion body: %v", err)
						return
					}
					if err := json.Unmarshal(raw, &body); err != nil {
						t.Errorf("invalid completion body %q: %v", raw, err)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintln(w, `data: {"content":"","stop":true}`)
				default:
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
			}))
			defer srv.Close()

			parts := strings.Split(srv.URL, ":")
			var port int
			fmt.Sscanf(parts[len(parts)-1], "%d", &port)

			runner := &llamaServerRunner{
				port:         port,
				cmd:          fakeRunningCmd(),
				sem:          semaphore.NewWeighted(1),
				options:      api.Options{Runner: api.Runner{NumCtx: 2048}},
				usedOpencoti: tt.opencoti,
			}
			runner.launch.config = tt.cfg

			opts := api.DefaultOptions()
			req := tt.req
			req.Options = &opts
			if err := runner.Completion(t.Context(), req, func(CompletionResponse) {}); err != nil {
				t.Fatalf("Completion error: %v", err)
			}

			for _, check := range []struct {
				field string
				want  any
			}{{"session_id", tt.wantID}, {"pool_id", tt.wantPool}} {
				got, ok := body[check.field]
				if check.want == nil {
					if ok {
						t.Errorf("%s reached the wire as %v; it must be absent", check.field, got)
					}
					continue
				}
				if !ok {
					t.Errorf("%s missing from the completion body", check.field)
					continue
				}
				if got != check.want {
					t.Errorf("%s = %v (%T), want %v (%T)", check.field, got, got, check.want, check.want)
				}
			}
		})
	}
}

func fmtPool(p *int) string {
	if p == nil {
		return "none"
	}
	return fmt.Sprintf("%d", *p)
}
