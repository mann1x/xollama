package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// xollama-hook: engine-introspect
//
// Reading back what the engine actually did.
//
// Every setting this fork adds is written into the engine's argv at launch and
// then disappears. The engine has its own HTTP surface that would answer for
// it -- /props carries the effective cache state, /slots the live slots,
// /polykv/pools the shared prefixes -- but that surface lives on a private
// localhost port that ollama picks at random and never publishes. So "did my
// setting apply?" has no answer short of finding the port in a log file.
//
// That gap is worse than an inconvenience for one specific reason: on settings
// the engine resolves for itself, its own report is the only authority. A cache
// type can be written into argv, accepted, and still be served by a different
// tier than the one named. Nothing on this side knows.
//
// This is the read-only window onto that. It proxies a fixed set of GET
// endpoints and nothing else -- no POST, no path the caller composes -- so it
// cannot become a way to drive the engine from outside.

// EngineIntrospector is implemented by a runner whose engine has an HTTP
// surface worth reading back.
//
// It is deliberately NOT part of the LlamaServer interface. Only the
// llama-server family has such a surface, the MLX runner has nothing to
// implement, and a method added there purely to return "unsupported" is a
// method upstream would have to carry through every merge.
type EngineIntrospector interface {
	// EngineName is which engine actually served this load.
	EngineName() string
	// EngineGet performs one read-only request against the engine and returns
	// its status and body unaltered.
	EngineGet(ctx context.Context, endpoint string) (int, []byte, error)
}

// introspectEndpoints is every path that may be reached, and there is no way to
// ask for one that is not here.
//
// A caller-composed path would make this a general proxy onto a process that
// trusts its caller completely: llama-server's own surface includes POST
// /completion and, on some builds, slot save and restore to arbitrary files. A
// whitelist of GETs is the difference between an introspection endpoint and a
// hole.
var introspectEndpoints = []string{
	// Upstream's, present on both engines.
	"props", "slots", "metrics",
	// opencoti's. Absent on stock, where the engine answers 404 and that 404
	// is itself the honest answer.
	"polykv/pools",
}

// defaultIntrospectEndpoint is what a caller who named none gets. /props is the
// one that answers "did my setting apply?", which is the question this exists
// for.
const defaultIntrospectEndpoint = "props"

// introspectTimeout bounds a read against an engine that is busy or wedged. It
// is short on purpose: this is a diagnostic, and a diagnostic that hangs is
// worse than one that says it could not tell.
const introspectTimeout = 5 * time.Second

// ErrUnknownIntrospectEndpoint is returned for a path outside the whitelist.
var ErrUnknownIntrospectEndpoint = errors.New("unknown engine endpoint")

// IntrospectEndpoints returns the readable endpoints, so a caller can be told
// what it may ask for rather than guessing.
func IntrospectEndpoints() []string { return slices.Clone(introspectEndpoints) }

// ValidIntrospectEndpoint normalises and checks one endpoint name.
func ValidIntrospectEndpoint(endpoint string) (string, error) {
	endpoint = strings.Trim(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return defaultIntrospectEndpoint, nil
	}
	if !slices.Contains(introspectEndpoints, endpoint) {
		return "", fmt.Errorf("%w %q (readable: %s)", ErrUnknownIntrospectEndpoint, endpoint, strings.Join(introspectEndpoints, ", "))
	}
	return endpoint, nil
}

// EngineName reports which engine served this runner.
func (s *llamaServerRunner) EngineName() string {
	if s.usedOpencoti {
		return "opencoti"
	}
	return "llamacpp"
}

// EngineGet performs one whitelisted read against the engine.
//
// The body comes back exactly as the engine wrote it, including a non-200. A
// 404 from an endpoint this engine does not have is information, not an error,
// and turning it into one here would hide the most useful answer the stock
// engine can give about an opencoti-only feature.
func (s *llamaServerRunner) EngineGet(ctx context.Context, endpoint string) (int, []byte, error) {
	endpoint, err := ValidIntrospectEndpoint(endpoint)
	if err != nil {
		return 0, nil, err
	}
	if s.port == 0 {
		return 0, nil, errors.New("engine is not listening yet")
	}

	ctx, cancel := context.WithTimeout(ctx, introspectTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/%s", s.port, endpoint), nil)
	if err != nil {
		return 0, nil, err
	}

	res, err := s.httpClient().Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("read %s from the engine: %w", endpoint, err)
	}
	defer res.Body.Close()

	// Bounded, because this is a diagnostic path reading a process that could
	// be producing anything. /metrics on a long-lived server is the realistic
	// large case and is far below this.
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return res.StatusCode, nil, fmt.Errorf("read %s from the engine: %w", endpoint, err)
	}
	return res.StatusCode, body, nil
}
