package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// xollama-hook: engine-introspect
//
// The engine's own surface, through the API a user already has.
//
// Every setting this fork adds is written into the engine's argv at launch and
// then disappears. The engine has its own HTTP surface that answers for it --
// /props carries the effective cache state, /slots the live slots, /kv the
// windows and the pressure, /polykv/pools the shared prefixes -- and drives
// it: pools, sessions, resizes, the elastic budget. But that surface lives on
// a private localhost port that ollama picks at random and never publishes.
//
// This is the window onto all of it: every opencoti management route, read and
// control, by name from a table of the engine's real routes. What stays out is
// what xollama already serves itself -- inference (/completion, the OpenAI and
// Anthropic shapes, embeddings), because a request behind the scheduler's back
// is work it cannot see -- and the engine's /cors-proxy and /tools, which
// fetch URLs of the caller's choosing when they are on at all.

// EngineIntrospector is implemented by a runner whose engine has an HTTP
// surface.
//
// It is deliberately NOT part of the LlamaServer interface. Only the
// llama-server family has such a surface, the MLX runner has nothing to
// implement, and a method added there purely to return "unsupported" is a
// method upstream would have to carry through every merge.
type EngineIntrospector interface {
	// EngineName is which engine actually served this load.
	EngineName() string
	// EngineDo performs one call against the engine's surface. The caller
	// closes the response body; the status and body are the engine's own.
	EngineDo(ctx context.Context, call EngineCall) (*http.Response, error)
}

// EngineCall is one request to the engine.
type EngineCall struct {
	Method   string
	Endpoint string
	// Query is forwarded as is (?live=1, ?once=1, ?action=…).
	Query url.Values
	Body  io.Reader
	// ContentType is the body's; application/json when empty.
	ContentType string
}

// engineRoute is one route the engine registers, by method. A {…} segment is
// a caller's id, held to engineIDSegment.
type engineRoute struct {
	method string
	path   string
}

// engineRoutes is every path that may be reached, and there is no way to ask
// for one that is not here. Mirrors the engine's registration in
// tools/server/server.cpp and docs/features/read_routes_audit.md; stock
// llama.cpp answers 404 for opencoti's, and that 404 is the honest answer.
var engineRoutes = []engineRoute{
	// Reads.
	{http.MethodGet, "props"},
	{http.MethodGet, "slots"},
	{http.MethodGet, "metrics"},
	{http.MethodGet, "health"},
	{http.MethodGet, "models"},
	{http.MethodGet, "lora-adapters"},
	{http.MethodGet, "kv"},
	{http.MethodGet, "elastic"},
	{http.MethodGet, "polykv/pools"},
	{http.MethodGet, "polykv/pools/{id}"},
	{http.MethodGet, "polykv/pools/{id}/capacity"},
	{http.MethodGet, "polykv/tps"},
	{http.MethodGet, "gpu/peers"},
	{http.MethodGet, "lock/status"},

	// Prompt building: what a PolyKV client needs to make a prefix.
	{http.MethodPost, "tokenize"},
	{http.MethodPost, "detokenize"},
	{http.MethodPost, "apply-template"},

	// Control.
	{http.MethodPost, "props"},
	{http.MethodPost, "lora-adapters"},
	{http.MethodPost, "slots/{id}"},
	{http.MethodPost, "polykv/pools"},
	{http.MethodPost, "polykv/pools/{id}/{action}"},
	{http.MethodDelete, "polykv/pools/{id}"},
	{http.MethodPost, "polykv/sampling"},
	{http.MethodPost, "elastic"},
	{http.MethodPost, "sessions/close"},
	{http.MethodPost, "sessions/{id}/close"},
	{http.MethodDelete, "sessions/{id}"},
	{http.MethodPost, "sessions/resize"},
	{http.MethodPost, "sessions/{id}/resize"},
	{http.MethodPost, "kv/sessions/{id}/resize"},
	{http.MethodPost, "lock/acquire"},
	{http.MethodPost, "lock/release"},
	{http.MethodPost, "lock/renew"},
}

// engineIDSegment is what may stand in a {…} segment: the engine's own id
// alphabet. An id with any other byte -- '/', '%', whitespace -- takes the
// body form the engine offers for it (/sessions/close, /sessions/resize).
var engineIDSegment = regexp.MustCompile(`^[A-Za-z0-9_~-][A-Za-z0-9._~-]{0,255}$`)

// defaultIntrospectEndpoint is what a caller who named none gets. /props is the
// one that answers "did my setting apply?".
const defaultIntrospectEndpoint = "props"

// Timeouts. A read is served from the engine's snapshot and answers at once;
// ?live=1 waits for a decode step. A control call can prefill (a pool built
// from tokens), so it gets as long as a prompt would. A stream has none.
const (
	engineReadTimeout    = 30 * time.Second
	engineControlTimeout = 10 * time.Minute
)

// ErrUnknownIntrospectEndpoint is returned for a path outside the table.
var ErrUnknownIntrospectEndpoint = errors.New("unknown engine endpoint")

// IntrospectEndpoints lists the reachable routes as "METHOD path", so a caller
// is told what it may ask for rather than guessing.
func IntrospectEndpoints() []string {
	out := make([]string, 0, len(engineRoutes))
	for _, r := range engineRoutes {
		out = append(out, r.method+" "+r.path)
	}
	return out
}

// ValidIntrospectEndpoint normalises and checks one GET endpoint.
func ValidIntrospectEndpoint(endpoint string) (string, error) {
	return ValidEngineEndpoint(http.MethodGet, endpoint)
}

// ValidEngineEndpoint normalises and checks one endpoint for a method.
func ValidEngineEndpoint(method, endpoint string) (string, error) {
	endpoint = strings.Trim(strings.TrimSpace(endpoint), "/")
	if endpoint == "" && method == http.MethodGet {
		return defaultIntrospectEndpoint, nil
	}
	segs := strings.Split(endpoint, "/")
	for _, r := range engineRoutes {
		if r.method == method && routeMatches(strings.Split(r.path, "/"), segs) {
			return endpoint, nil
		}
	}
	return "", fmt.Errorf("%w %s %q (see GET /api/engine for the list)", ErrUnknownIntrospectEndpoint, method, endpoint)
}

func routeMatches(pattern, segs []string) bool {
	if len(pattern) != len(segs) {
		return false
	}
	for i, p := range pattern {
		if strings.HasPrefix(p, "{") {
			if !engineIDSegment.MatchString(segs[i]) {
				return false
			}
		} else if p != segs[i] {
			return false
		}
	}
	return true
}

// EngineName reports which engine served this runner.
func (s *llamaServerRunner) EngineName() string {
	if s.usedOpencoti {
		return "opencoti"
	}
	return "llamacpp"
}

// EngineDo performs one call from the table against the engine.
//
// The response comes back exactly as the engine wrote it, including a non-200.
// A 404 from a route this engine does not have is information, not an error.
func (s *llamaServerRunner) EngineDo(ctx context.Context, call EngineCall) (*http.Response, error) {
	method := call.Method
	if method == "" {
		method = http.MethodGet
	}
	endpoint, err := ValidEngineEndpoint(method, call.Endpoint)
	if err != nil {
		return nil, err
	}
	if s.port == 0 {
		return nil, errors.New("engine is not listening yet")
	}

	cancel := context.CancelFunc(func() {})
	switch {
	case endpoint == "polykv/tps" && call.Query.Get("once") == "":
		// A stream: it lasts as long as the caller listens.
	case method == http.MethodGet:
		ctx, cancel = context.WithTimeout(ctx, engineReadTimeout)
	default:
		ctx, cancel = context.WithTimeout(ctx, engineControlTimeout)
	}

	u := url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", s.port), Path: "/" + endpoint, RawQuery: call.Query.Encode()}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), call.Body)
	if err != nil {
		cancel()
		return nil, err
	}
	if call.Body != nil {
		ct := call.ContentType
		if ct == "" {
			ct = "application/json"
		}
		req.Header.Set("Content-Type", ct)
	}

	res, err := s.httpClient().Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%s %s on the engine: %w", method, endpoint, err)
	}
	res.Body = cancelOnClose{res.Body, cancel}
	return res, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// engineRoutesFor is the table's methods for one path pattern, for tests.
func engineRoutesFor(path string) []string {
	var out []string
	for _, r := range engineRoutes {
		if r.path == path {
			out = append(out, r.method)
		}
	}
	slices.Sort(out)
	return out
}
