package llm

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/ollama/ollama/llm/engine"
)

// FeatureGracefulShutdown is opencoti's POST /shutdown (patch 0555, asked for
// in our #759): the same path as SIGTERM, which a parent can reach on every
// OS. Windows has no SIGTERM for a child, and TerminateProcess leaves the GPU
// devices alive.
const featureGracefulShutdown = "graceful_shutdown_v1"

// gracefulStopWait bounds the wait for the engine to exit after /shutdown;
// it is the engine's own deadline (0555: 10 s), after which the kill follows.
var gracefulStopWait = 10 * time.Second

// hasGracefulShutdown is engine.HasPolicy for the feature; a var so a test
// can state either answer.
var hasGracefulShutdown = func() bool { return engine.HasPolicy(featureGracefulShutdown) }

// stopGracefully asks an opencoti engine to shut down and waits for it to
// exit, up to gracefulStopWait. A clean exit is what writes the MoE cache's
// warm-start profile (c14 r19), and lets the engine idle and release its GPU
// devices itself. It reports whether the process has exited; the caller
// kills it otherwise, as before. Stock llama.cpp, an engine without the
// route, an engine still loading (the route answers only once the server is
// up) and a refused request all return false at once.
func (s *llamaServerRunner) stopGracefully() bool {
	if !s.usedOpencoti || s.port == 0 || s.done == nil || !hasGracefulShutdown() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	status, _, err := s.engineRequest(ctx, http.MethodPost, "/shutdown", nil)
	if err != nil || status != http.StatusOK {
		slog.Debug("engine shutdown request not taken; stopping the process", "status", status, "error", err)
		return false
	}
	select {
	case <-s.done:
		return true
	case <-time.After(gracefulStopWait):
		slog.Warn("the engine did not exit after its shutdown request; stopping the process", "pid", s.Pid(), "waited", gracefulStopWait)
		return false
	}
}
