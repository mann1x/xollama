package llm

// xollama: a health check that could not reach a live opencoti engine is
// tried again -- the `engine-health-retry` hook in getServerStatusRetry.
//
// Upstream fails the request on the first failed dial to the engine's
// /health. On eleven2go (a0968aea, hard, council run 3) one dial to a live,
// serving opencoti timed out on loopback ("connectex: A connection attempt
// failed because the connected party did not properly respond"); the engine
// answered the next request three seconds later, but the council's turn was
// lost with it. Stock llama.cpp keeps upstream's behaviour.

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"
)

// healthRetries is how many more times a failed dial is tried, healthBackoff
// the first wait, doubled each time (0.25 + 0.5 + 1 + 2 s).
var (
	healthRetries = 4
	healthBackoff = 250 * time.Millisecond
)

// healthDialFailed reports whether a health check failed before the engine
// answered at all: the dial failed, or it was refused.
func healthDialFailed(status ServerStatus, err error) bool {
	if err == nil {
		return false
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	return status == ServerStatusNotResponding && err.Error() == "connection refused"
}

// retryHealth is getServerStatusRetry's: on opencoti, with the engine process
// still running, a check that failed to reach it is asked again with backoff.
// Anything else -- another engine, an engine that exited, an answer that was
// an error, a caller that gave up -- is returned as it came.
func (s *llamaServerRunner) retryHealth(ctx context.Context, status ServerStatus, err error) (ServerStatus, error) {
	wait := healthBackoff
	for try := 1; try <= healthRetries && s.usedOpencoti && s.cmd != nil && s.cmd.ProcessState == nil && healthDialFailed(status, err); try++ {
		slog.Warn("engine health check could not reach the engine; trying again", "try", try, "wait", wait, "error", err)
		select {
		case <-ctx.Done():
			return status, err
		case <-time.After(wait):
		}
		wait *= 2
		status, err = s.getServerStatus(ctx)
	}
	return status, err
}
