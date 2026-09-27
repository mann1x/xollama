package llm

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestValidIntrospectEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"empty defaults to props", "", "props"},
		{"whitespace defaults to props", "   ", "props"},
		{"a leading slash is tolerated", "/props", "props"},
		{"a trailing slash is tolerated", "slots/", "slots"},
		{"an opencoti path with a slash inside it", "polykv/pools", "polykv/pools"},
		{"metrics", "metrics", "metrics"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidIntrospectEndpoint(tc.in)
			if err != nil {
				t.Fatalf("ValidIntrospectEndpoint(%q) = %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ValidIntrospectEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestValidIntrospectEndpointRefusesAnythingElse is the whole reason the path
// is a name from a list rather than something the caller composes.
//
// llama-server trusts whoever can reach it: its surface includes POST
// /completion, /apply-template, and on some builds slot save and restore to
// file paths of the caller's choosing. A proxy that forwarded an arbitrary path
// would hand all of that to anyone who can reach the ollama port, which is a
// much larger set of people.
func TestValidIntrospectEndpointRefusesAnythingElse(t *testing.T) {
	for _, endpoint := range []string{
		"completion",
		"apply-template",
		"embedding",
		"slots/0?action=restore",
		"../../../etc/passwd",
		"..%2f..%2fprops",
		"props/../completion",
		"health\r\nX-Injected: 1",
		"http://elsewhere.example/props",
		"//elsewhere.example/props",
	} {
		t.Run(endpoint, func(t *testing.T) {
			got, err := ValidIntrospectEndpoint(endpoint)
			if err == nil {
				t.Fatalf("ValidIntrospectEndpoint(%q) = %q, want a refusal", endpoint, got)
			}
			if !errors.Is(err, ErrUnknownIntrospectEndpoint) {
				t.Errorf("error = %v, want it to wrap ErrUnknownIntrospectEndpoint so a caller can tell it from a transport failure", err)
			}
		})
	}
}

// TestEveryOpencotiRouteIsReachable pins the table to the engine's own
// registration (tools/server/server.cpp): each management route, by method.
func TestEveryOpencotiRouteIsReachable(t *testing.T) {
	for path, want := range map[string][]string{
		"props":                      {"GET", "POST"},
		"slots":                      {"GET"},
		"metrics":                    {"GET"},
		"kv":                         {"GET"},
		"elastic":                    {"GET", "POST"},
		"polykv/pools":               {"GET", "POST"},
		"polykv/pools/{id}":          {"DELETE", "GET"},
		"polykv/pools/{id}/capacity": {"GET"},
		"polykv/pools/{id}/{action}": {"POST"},
		"polykv/tps":                 {"GET"},
		"polykv/sampling":            {"POST"},
		"sessions/close":             {"POST"},
		"sessions/{id}/close":        {"POST"},
		"sessions/{id}":              {"DELETE"},
		"sessions/resize":            {"POST"},
		"sessions/{id}/resize":       {"POST"},
		"kv/sessions/{id}/resize":    {"POST"},
		"lock/status":                {"GET"},
		"lock/acquire":               {"POST"},
		"gpu/peers":                  {"GET"},
		"apply-template":             {"POST"},
		"tokenize":                   {"POST"},
	} {
		if got := engineRoutesFor(path); !slices.Equal(got, want) {
			t.Errorf("%s: methods %v, want %v", path, got, want)
		}
	}
	for _, e := range []struct{ method, endpoint string }{
		{"GET", "kv"},
		{"GET", "polykv/pools/0"},
		{"GET", "polykv/pools/12/capacity"},
		{"POST", "polykv/pools/0/fork"},
		{"DELETE", "polykv/pools/7"},
		{"POST", "sessions/chat-1~researcher-2/resize"},
		{"DELETE", "sessions/chat.1"},
	} {
		if _, err := ValidEngineEndpoint(e.method, e.endpoint); err != nil {
			t.Errorf("%s %s refused: %v", e.method, e.endpoint, err)
		}
	}
}

// TestInferenceStaysBehindTheScheduler: xollama serves inference itself, and a
// request behind the scheduler's back is work it cannot account for.
func TestInferenceStaysBehindTheScheduler(t *testing.T) {
	for _, endpoint := range []string{
		"completion", "completions", "v1/completions", "chat/completions", "v1/chat/completions",
		"v1/responses", "v1/messages", "embedding", "embeddings", "v1/embeddings", "rerank", "infill",
		"cors-proxy", "tools", "models", "models/load", "v1/streams/lookup",
	} {
		if _, err := ValidEngineEndpoint("POST", endpoint); !errors.Is(err, ErrUnknownIntrospectEndpoint) {
			t.Errorf("POST %s: err %v, want a refusal", endpoint, err)
		}
	}
	if !slices.Contains(IntrospectEndpoints(), "POST polykv/pools/{id}/{action}") {
		t.Error("the listing must name the control routes with their methods")
	}
}

func TestEngineName(t *testing.T) {
	if got := (&llamaServerRunner{usedOpencoti: true}).EngineName(); got != "opencoti" {
		t.Errorf("EngineName() = %q, want opencoti", got)
	}
	if got := (&llamaServerRunner{}).EngineName(); got != "llamacpp" {
		t.Errorf("EngineName() = %q, want llamacpp", got)
	}
}

// TestEngineDoBeforeTheEngineListens covers the window between a runner
// existing and its process being up.
func TestEngineDoBeforeTheEngineListens(t *testing.T) {
	res, err := (&llamaServerRunner{}).EngineDo(t.Context(), EngineCall{Endpoint: "props"})
	if err == nil {
		res.Body.Close()
		t.Fatal("EngineDo() on a runner with no port = nil error, want a refusal")
	}
}

// TestEngineDoValidatesBeforeDialing proves the refusal happens before any
// network work, so a bad path cannot reach the engine even in principle.
func TestEngineDoValidatesBeforeDialing(t *testing.T) {
	// Port 1 would fail to connect; the table must reject first.
	res, err := (&llamaServerRunner{port: 1}).EngineDo(t.Context(), EngineCall{Method: "POST", Endpoint: "completion"})
	if err == nil {
		res.Body.Close()
	}
	if !errors.Is(err, ErrUnknownIntrospectEndpoint) {
		t.Fatalf("error = %v, want the table's refusal rather than a dial failure", err)
	}
}

// TestEngineDoOnTheWire: what the engine receives is the method, the path, the
// caller's query and body, and a JSON content type.
func TestEngineDoOnTheWire(t *testing.T) {
	var got struct{ method, path, query, ct, body string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.method, got.path, got.query, got.ct, got.body = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Content-Type"), string(b)
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"queued":true}`)
	}))
	defer srv.Close()
	port, _ := strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])

	s := &llamaServerRunner{port: port}
	res, err := s.EngineDo(t.Context(), EngineCall{
		Method: "POST", Endpoint: "/sessions/chat-1/resize",
		Query: url.Values{"live": {"1"}}, Body: strings.NewReader(`{"num_ctx":8192}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusAccepted || string(body) != `{"queued":true}` {
		t.Errorf("answer %d %s, want the engine's own", res.StatusCode, body)
	}
	if got.method != "POST" || got.path != "/sessions/chat-1/resize" || got.query != "live=1" ||
		got.ct != "application/json" || got.body != `{"num_ctx":8192}` {
		t.Errorf("engine received %+v", got)
	}
}
