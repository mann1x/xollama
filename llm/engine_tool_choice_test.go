package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

// tokenizerRunner answers /tokenize with the tokens vocab names for the
// content, and records whether special tokens were asked to be parsed.
func tokenizerRunner(t *testing.T, vocab map[string][]int, parseSpecial *bool) *llamaServerRunner {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Content      string `json:"content"`
			ParseSpecial bool   `json:"parse_special"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		*parseSpecial = body.ParseSpecial
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": vocab[body.Content]})
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	return &llamaServerRunner{port: port}
}

func TestToolChoiceNoneBansAOneTokenTag(t *testing.T) {
	var special bool
	s := tokenizerRunner(t, map[string][]int{"<|tool_call>": {48}}, &special)

	req := llamaServerCompletionRequest{Stop: []string{"<eos>"}}
	s.banToolCall(context.Background(), "<|tool_call>").apply(&req)

	if !special {
		t.Fatal("the tag was not tokenized as a special token")
	}
	out, _ := json.Marshal(req)
	if !strings.Contains(string(out), `"logit_bias":[[48,false]]`) {
		t.Fatalf("body %s does not ban token 48", out)
	}
	if len(req.Stop) != 1 {
		t.Fatalf("stop = %v, want the request's own only", req.Stop)
	}
}

func TestToolChoiceNoneEndsTheAnswerAtATagOfSeveralTokens(t *testing.T) {
	var special bool
	s := tokenizerRunner(t, map[string][]int{"<function=": {27, 1688, 28}}, &special)

	stop := []string{"<eos>"}
	req := llamaServerCompletionRequest{Stop: stop}
	s.banToolCall(context.Background(), "<function=").apply(&req)

	if req.LogitBias != nil {
		t.Fatalf("logit_bias = %v; a tag's pieces must not be banned", req.LogitBias)
	}
	if want := []string{"<eos>", "<function="}; strings.Join(req.Stop, "|") != strings.Join(want, "|") {
		t.Fatalf("stop = %v, want %v", req.Stop, want)
	}
	if len(stop) != 1 {
		t.Fatal("the caller's stop list was changed in place")
	}
}

func TestToolChoiceNoneLeavesAPlainTextOpenerAlone(t *testing.T) {
	var special bool
	s := tokenizerRunner(t, map[string][]int{"{": {90}}, &special)

	req := llamaServerCompletionRequest{}
	s.banToolCall(context.Background(), "{").apply(&req)
	if req.LogitBias != nil || req.Stop != nil {
		t.Fatalf("a brace was banned: bias %v stop %v", req.LogitBias, req.Stop)
	}
}

func TestACompletionWithoutToolChoiceNoneSendsNoBias(t *testing.T) {
	out, _ := json.Marshal(llamaServerCompletionRequest{})
	if strings.Contains(string(out), "logit_bias") {
		t.Fatalf("body %s carries logit_bias", out)
	}
}

func TestChatBodyToolChoiceNone(t *testing.T) {
	tools := api.Tools{{Type: "function", Function: api.ToolFunction{Name: "editor"}}}
	for _, tt := range []struct {
		name  string
		none  bool
		tools api.Tools
		want  any
	}{
		{"none with tools", true, tools, "none"},
		{"none without tools", true, nil, nil},
		{"calls allowed", false, tools, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &llamaServerRunner{}
			body, err := s.llamaServerChatRequest(ChatRequest{
				Messages:       []api.Message{{Role: "user", Content: "hi"}},
				Tools:          tt.tools,
				ToolChoiceNone: tt.none,
			}, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := body["tool_choice"]; got != tt.want {
				t.Fatalf("tool_choice = %v, want %v", got, tt.want)
			}
		})
	}
}
