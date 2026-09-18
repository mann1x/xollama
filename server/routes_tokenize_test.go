package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
)

// newTokenizeServer returns a server with one loadable model called "tok",
// which is all either handler needs: they require no capability.
func newTokenizeServer(t *testing.T, mock *mockRunner) *Server {
	t.Helper()

	gin.SetMode(gin.TestMode)
	s := newServerWithMockRunner(t, mock)
	createMinimalGGUFModel(t, s, "tok", nil, "{{ .Prompt }}", nil)
	return s
}

func TestTokenizeHandler(t *testing.T) {
	mock := mockRunner{}
	s := newTokenizeServer(t, &mock)

	w := createRequest(t, s.TokenizeHandler, api.TokenizeRequest{
		Model: "tok",
		Input: "one two three",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var resp api.TokenizeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// mockRunner tokenizes on whitespace, one token per field.
	if want := []int{0, 1, 2}; !slices.Equal(resp.Tokens, want) {
		t.Errorf("tokens = %v, want %v", resp.Tokens, want)
	}
	if resp.Model != "tok" {
		t.Errorf("model = %q, want %q", resp.Model, "tok")
	}
}

func TestDetokenizeHandler(t *testing.T) {
	mock := mockRunner{
		DetokenizeFn: func(_ context.Context, tokens []int) (string, error) {
			if !slices.Equal(tokens, []int{0, 1, 2}) {
				t.Errorf("handler passed tokens %v", tokens)
			}
			return "one two three", nil
		},
	}
	s := newTokenizeServer(t, &mock)

	w := createRequest(t, s.DetokenizeHandler, api.DetokenizeRequest{
		Model:  "tok",
		Tokens: []int{0, 1, 2},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var resp api.DetokenizeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Text != "one two three" {
		t.Errorf("text = %q, want %q", resp.Text, "one two three")
	}
	if resp.Model != "tok" {
		t.Errorf("model = %q, want %q", resp.Model, "tok")
	}
}

// An empty input is how a client asks for the model to be loaded without
// tokenizing anything, matching /api/embeddings. It must not reach the runner
// and must not be an error.
func TestTokenizeEmptyInputLoadsTheModel(t *testing.T) {
	mock := mockRunner{
		TokenizeFn: func(context.Context, string) ([]int, error) {
			t.Error("empty input reached the runner")
			return nil, nil
		},
	}
	s := newTokenizeServer(t, &mock)

	w := createRequest(t, s.TokenizeHandler, api.TokenizeRequest{Model: "tok"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var resp api.TokenizeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Tokens) != 0 {
		t.Errorf("tokens = %v, want empty", resp.Tokens)
	}
	// An empty list, not a JSON null -- a client ranging over it should not
	// have to nil-check.
	if raw := gjsonTokens(t, w.Body.Bytes()); raw != `[]` {
		t.Errorf("tokens serialised as %s, want []", raw)
	}
}

func TestDetokenizeEmptyTokensLoadsTheModel(t *testing.T) {
	mock := mockRunner{
		DetokenizeFn: func(context.Context, []int) (string, error) {
			t.Error("empty token list reached the runner")
			return "", nil
		},
	}
	s := newTokenizeServer(t, &mock)

	w := createRequest(t, s.DetokenizeHandler, api.DetokenizeRequest{Model: "tok"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var resp api.DetokenizeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Text != "" {
		t.Errorf("text = %q, want empty", resp.Text)
	}
}

func TestTokenizeRoundTrip(t *testing.T) {
	var captured []int
	mock := mockRunner{
		DetokenizeFn: func(_ context.Context, tokens []int) (string, error) {
			captured = tokens
			return "round trip", nil
		},
	}
	s := newTokenizeServer(t, &mock)

	w := createRequest(t, s.TokenizeHandler, api.TokenizeRequest{Model: "tok", Input: "round trip"})
	if w.Code != http.StatusOK {
		t.Fatalf("tokenize status = %d: %s", w.Code, w.Body.String())
	}
	var tok api.TokenizeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &tok); err != nil {
		t.Fatal(err)
	}

	w = createRequest(t, s.DetokenizeHandler, api.DetokenizeRequest{Model: "tok", Tokens: tok.Tokens})
	if w.Code != http.StatusOK {
		t.Fatalf("detokenize status = %d: %s", w.Code, w.Body.String())
	}
	if !slices.Equal(captured, tok.Tokens) {
		t.Errorf("detokenize received %v, tokenize produced %v", captured, tok.Tokens)
	}
}

func TestTokenizeHandlerRejectsBadRequests(t *testing.T) {
	mock := mockRunner{}
	s := newTokenizeServer(t, &mock)

	cases := []struct {
		name string
		fn   func(*gin.Context)
		body any
	}{
		{"tokenize without a model", s.TokenizeHandler, api.TokenizeRequest{Input: "hello"}},
		{"detokenize without a model", s.DetokenizeHandler, api.DetokenizeRequest{Tokens: []int{0}}},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			w := createRequest(t, tt.fn, tt.body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestTokenizeHandlerReportsRunnerFailure(t *testing.T) {
	mock := mockRunner{
		TokenizeFn: func(context.Context, string) ([]int, error) {
			return nil, errors.New("tokenizer exploded")
		},
	}
	s := newTokenizeServer(t, &mock)

	w := createRequest(t, s.TokenizeHandler, api.TokenizeRequest{Model: "tok", Input: "hello"})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
}

func TestDetokenizeHandlerReportsRunnerFailure(t *testing.T) {
	mock := mockRunner{
		DetokenizeFn: func(context.Context, []int) (string, error) {
			return "", errors.New("detokenizer exploded")
		},
	}
	s := newTokenizeServer(t, &mock)

	w := createRequest(t, s.DetokenizeHandler, api.DetokenizeRequest{Model: "tok", Tokens: []int{0}})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
}

// gjsonTokens returns the raw JSON of the "tokens" field, to tell [] from null.
func gjsonTokens(t *testing.T, body []byte) string {
	t.Helper()
	var raw struct {
		Tokens json.RawMessage `json:"tokens"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	return string(raw.Tokens)
}
