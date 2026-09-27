package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/model"
	"github.com/ollama/ollama/types/xollama"
)

type runHits struct {
	mu       sync.Mutex
	chat     []api.ChatRequest
	generate int
}

func councilRunServer(t *testing.T, cfg *xollama.Config) *runHits {
	t.Helper()
	hits := &runHits{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			_ = json.NewEncoder(w).Encode(api.ShowResponse{
				Capabilities: []model.Capability{model.CapabilityCompletion},
				Xollama:      cfg,
			})
		case "/api/chat":
			var req api.ChatRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			hits.mu.Lock()
			hits.chat = append(hits.chat, req)
			hits.mu.Unlock()
			enc := json.NewEncoder(w)
			_ = enc.Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Thinking: "### Researcher 1\nfound it\n"}})
			_ = enc.Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "the council's answer"}, Done: true})
		case "/api/generate":
			hits.mu.Lock()
			hits.generate++
			hits.mu.Unlock()
			_ = json.NewEncoder(w).Encode(api.GenerateResponse{Response: "the plain model", Done: true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Setenv("XOLLAMA_HOST", srv.URL)
	t.Cleanup(srv.Close)
	return hits
}

func runOnce(t *testing.T, format string, args ...string) string {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.Flags().String("keepalive", "", "")
	cmd.Flags().Bool("truncate", false, "")
	cmd.Flags().Int("dimensions", 0, "")
	cmd.Flags().Bool("verbose", false, "")
	cmd.Flags().Bool("insecure", false, "")
	cmd.Flags().Bool("nowordwrap", false, "")
	cmd.Flags().String("format", format, "")
	cmd.Flags().String("think", "", "")
	cmd.Flags().Bool("hidethinking", false, "")

	oldStdout := os.Stdout
	readOut, writeOut, _ := os.Pipe()
	os.Stdout = writeOut
	t.Cleanup(func() { os.Stdout = oldStdout })

	err := RunHandler(cmd, args)
	_ = writeOut.Close()
	var out bytes.Buffer
	_, _ = io.Copy(&out, readOut)
	if err != nil {
		t.Fatalf("RunHandler: %v", err)
	}
	return out.String()
}

func councilConfig(on bool) *xollama.Config {
	return &xollama.Config{Version: 4, Council: &xollama.Council{Enabled: &on}}
}

// TestAOneShotPromptReachesTheCouncil: `xollama run <council> "…"` used to go
// to /api/generate and get the plain model, silently.
func TestAOneShotPromptReachesTheCouncil(t *testing.T) {
	hits := councilRunServer(t, councilConfig(true))
	out := runOnce(t, "", "my-council", "why", "is", "the", "sky", "blue?")

	if hits.generate != 0 {
		t.Errorf("a council model's one-shot prompt went to /api/generate %d times", hits.generate)
	}
	if len(hits.chat) != 1 {
		t.Fatalf("chat calls = %d, want 1", len(hits.chat))
	}
	msgs := hits.chat[0].Messages
	if len(msgs) != 1 || msgs[0].Role != "user" || msgs[0].Content != "why is the sky blue?" {
		t.Errorf("messages = %+v, want the prompt as the one user turn", msgs)
	}
	if !strings.Contains(out, "the council's answer") {
		t.Errorf("output %q, want the council's answer", out)
	}
}

// TestAPlainModelKeepsGenerate: off means off.
func TestAPlainModelKeepsGenerate(t *testing.T) {
	for name, cfg := range map[string]*xollama.Config{"no config": nil, "council off": councilConfig(false)} {
		t.Run(name, func(t *testing.T) {
			hits := councilRunServer(t, cfg)
			runOnce(t, "", "plain", "hi")
			if hits.generate != 1 || len(hits.chat) != 0 {
				t.Errorf("generate %d, chat %d: want upstream's one generate", hits.generate, len(hits.chat))
			}
		})
	}
}

// TestAFormatStaysOnGenerate: the council steps aside for a format anyway.
func TestAFormatStaysOnGenerate(t *testing.T) {
	hits := councilRunServer(t, councilConfig(true))
	runOnce(t, "json", "my-council", "list three colours")
	if hits.generate != 1 || len(hits.chat) != 0 {
		t.Errorf("generate %d, chat %d: a format keeps generate", hits.generate, len(hits.chat))
	}
}
