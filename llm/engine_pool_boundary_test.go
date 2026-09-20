package llm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

// templateEngine is a stand-in engine that owns a real template and a real
// tokeniser. A stub that returns canned tokens cannot exercise this code at
// all: the whole question is where one rendering stops agreeing with another,
// so the renderings have to be produced from the messages.
type templateEngine struct{}

func (templateEngine) render(msgs []message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString("<|" + m.Role + "|> " + m.Content + " ")
	}
	b.WriteString("<|assistant|>")
	return b.String()
}

// tokenize splits on whitespace and hashes each word. Word-per-token is not how
// a real tokeniser behaves, but it preserves the only property under test: the
// same text yields the same tokens in the same positions.
func (templateEngine) tokenize(prompt string) []int {
	fields := strings.Fields(prompt)
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		h := 7
		for _, c := range f {
			h = h*31 + int(c)
		}
		out = append(out, h)
	}
	return out
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func templateRunner(t *testing.T, recurrent bool) (*llamaServerRunner, *poolStub) {
	t.Helper()

	var eng templateEngine
	stub := &poolStub{payload: `{"pool_id":0}`}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		stub.mu.Lock()
		stub.paths = append(stub.paths, r.Method+" "+r.URL.Path)
		stub.bodies = append(stub.bodies, string(body))
		stub.mu.Unlock()

		switch r.URL.Path {
		case "/apply-template":
			var req struct {
				Messages []message `json:"messages"`
			}
			_ = json.Unmarshal(body, &req)
			_ = json.NewEncoder(w).Encode(map[string]string{"prompt": eng.render(req.Messages)})
		case "/tokenize":
			var req struct {
				Content string `json:"content"`
			}
			_ = json.Unmarshal(body, &req)
			_ = json.NewEncoder(w).Encode(map[string][]int{"tokens": eng.tokenize(req.Content)})
		default:
			_, _ = io.WriteString(w, `{"pool_id":0}`)
		}
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}

	s := &llamaServerRunner{port: port, usedOpencoti: true, pools: newPoolRegistry(2)}
	s.launch.recurrentState = recurrent
	return s, stub
}

// longSystem is a system prompt long enough to clear minPoolPrefixTokens, so
// these tests exercise the real threshold rather than a lowered one.
func longSystem() string {
	return strings.TrimSpace(strings.Repeat("policy ", 300))
}

func chatWith(question string) *ChatRequest {
	return &ChatRequest{Messages: []api.Message{
		{Role: "system", Content: longSystem()},
		{Role: "user", Content: question},
	}}
}

// TestTemplateBoundaryStopsWhereTheConversationStarts measures the boundary
// directly. It must cover the system marker, the system prompt and the marker
// that opens the user turn -- and must stop there, carrying no word of any
// probe.
func TestTemplateBoundaryStopsWhereTheConversationStarts(t *testing.T) {
	s, _ := templateRunner(t, false)

	boundary, err := s.templateBoundary(t.Context(), poolSource{chat: chatWith("What is the capital of France")})
	if err != nil {
		t.Fatal(err)
	}

	// <|system|> + 300 words + <|user|>.
	const want = 1 + 300 + 1
	if len(boundary) != want {
		t.Fatalf("boundary = %d tokens, want %d", len(boundary), want)
	}

	var eng templateEngine
	for _, probe := range PoolProbeContents {
		tok := eng.tokenize(probe)[0]
		for i, got := range boundary {
			if got == tok {
				t.Errorf("boundary token %d is the probe %q; the template must not include what follows it", i, probe)
			}
		}
	}
}

// TestPoolStopsAtTheTemplateNotAtWhatTwoUsersHappenedToShare is the overshoot
// itself. Both conversations open "What is", so the longest common prefix of
// the two real prompts runs two tokens past the template. On an attention model
// that is waste; on a recurrent one it is fatal, because the engine takes a
// share only when the match covers the pool entirely, so a pool two tokens long
// in the wrong direction can never be matched by a third conversation.
func TestPoolStopsAtTheTemplateNotAtWhatTwoUsersHappenedToShare(t *testing.T) {
	for _, recurrent := range []bool{false, true} {
		name := "attention"
		if recurrent {
			name = "recurrent"
		}
		t.Run(name, func(t *testing.T) {
			s, stub := templateRunner(t, recurrent)

			s.capturePool("k", poolSource{chat: chatWith("What is the capital of France")})
			waitForPoolCalls(t, stub, 1)
			s.capturePool("k", poolSource{chat: chatWith("What is two plus two")})

			calls := polykvCalls(t, stub, 1)
			if calls[0] != "POST /polykv/pools" {
				t.Fatalf("engine saw %v, want a pool create", calls)
			}

			var created struct {
				Tokens []int `json:"tokens"`
			}
			stub.mu.Lock()
			bodies := append([]string(nil), stub.bodies...)
			stub.mu.Unlock()
			for _, b := range bodies {
				if strings.Contains(b, `"tokens"`) {
					_ = json.Unmarshal([]byte(b), &created)
				}
			}

			const wantTemplate = 1 + 300 + 1
			if len(created.Tokens) != wantTemplate {
				t.Errorf("pooled %d tokens, want %d — the two shared %q beyond the template",
					len(created.Tokens), wantTemplate, "What is")
			}
		})
	}
}

// TestRecurrentModelIsNotPooledWithoutAMeasuredBoundary guards the case where
// nothing can say where the template stops: a prompt rendered elsewhere, with
// no probe renderings carried in. The only prefix available is then what two
// conversations happened to share, which is exactly what cannot be trusted on
// a model that shares its state only as a whole.
func TestRecurrentModelIsNotPooledWithoutAMeasuredBoundary(t *testing.T) {
	s, stub := templateRunner(t, true)

	prefix := longSystem()
	s.capturePool("k", poolSource{prompt: prefix + " What is the capital of France"})
	waitForPoolCalls(t, stub, 1)
	s.capturePool("k", poolSource{prompt: prefix + " What is two plus two"})
	waitForPoolCalls(t, stub, 2)

	stub.mu.Lock()
	paths := append([]string(nil), stub.paths...)
	stub.mu.Unlock()
	for _, p := range paths {
		if strings.Contains(p, "/polykv/pools") {
			t.Fatalf("created a pool for a recurrent model from a completion: %v", paths)
		}
	}
}

// TestPoolStopsAtTheTemplateOnTheOllamaRenderedPath is the path this feature
// actually runs on, and the one a live run caught being excluded.
//
// ollama renders the prompt itself for any model with a renderer, a parser,
// harmony or a Modelfile TEMPLATE -- which is most of them -- and calls the
// completion path. The engine's own template is then the wrong ruler, so the
// server package renders the probes against ollama's template and carries them
// in. Same overshoot, same trim, on the path that matters.
func TestPoolStopsAtTheTemplateOnTheOllamaRenderedPath(t *testing.T) {
	var eng templateEngine

	render := func(question string) string {
		return eng.render([]message{
			{Role: "system", Content: longSystem()},
			{Role: "user", Content: question},
		})
	}
	probes := make([]string, 0, len(PoolProbeContents))
	for _, p := range PoolProbeContents {
		probes = append(probes, render(p))
	}

	s, stub := templateRunner(t, true) // recurrent: the exacting case

	s.capturePool("k", poolSource{prompt: render("What is the capital of France"), probes: probes})
	waitForPoolCalls(t, stub, 1)
	s.capturePool("k", poolSource{prompt: render("What is two plus two"), probes: probes})

	if calls := polykvCalls(t, stub, 1); calls[0] != "POST /polykv/pools" {
		t.Fatalf("engine saw %v, want a pool create on the ollama-rendered path", calls)
	}

	var created struct {
		Tokens []int `json:"tokens"`
	}
	stub.mu.Lock()
	bodies := append([]string(nil), stub.bodies...)
	stub.mu.Unlock()
	for _, b := range bodies {
		if strings.Contains(b, `"tokens"`) {
			_ = json.Unmarshal([]byte(b), &created)
		}
	}
	const wantTemplate = 1 + 300 + 1
	if len(created.Tokens) != wantTemplate {
		t.Errorf("pooled %d tokens, want %d", len(created.Tokens), wantTemplate)
	}
}

// TestAttentionModelIsStillPooledFromACompletion keeps the guard above from
// quietly becoming a ban on completion pooling altogether.
func TestAttentionModelIsStillPooledFromACompletion(t *testing.T) {
	s, stub := templateRunner(t, false)

	prefix := longSystem()
	s.capturePool("k", poolSource{prompt: prefix + " What is the capital of France"})
	waitForPoolCalls(t, stub, 1)
	s.capturePool("k", poolSource{prompt: prefix + " What is two plus two"})

	if calls := polykvCalls(t, stub, 1); calls[0] != "POST /polykv/pools" {
		t.Fatalf("engine saw %v, want a pool create", calls)
	}
}
