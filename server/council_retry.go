package server

// xollama: every council member call is asked again when it fails or stalls
// -- plans/agentic-council-chat.md 11.29. Additive; reached from
// councilMembers.StreamTools and stream in server/council.go.
//
// One wrapper around the one place every member call passes, in place of a
// fallback written into each path that calls a member (LangChain's
// ModelRetryMiddleware, LangGraph's RetryPolicy and TimeoutPolicy). The
// council's own fallback -- a researcher or critic on another model answered
// by the council's -- still follows in internal/council, after these tries.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/internal/council"
	"github.com/ollama/ollama/llm"
)

var (
	// councilIdleTimeout ends a member call that has sent nothing for this
	// long. It covers a model load and a long prefill, which send nothing
	// either; on 20260930-085958 a cloud critic hung about 15 minutes before
	// its error came back and the fallback began.
	councilIdleTimeout = 5 * time.Minute
	// councilCallTimeout bounds one member call however much it streams.
	councilCallTimeout = 20 * time.Minute
	// councilRetries is how often a failed member call is asked again.
	councilRetries = 2
	// councilRetryBackoff is the wait before the first retry; it doubles.
	councilRetryBackoff = 2 * time.Second
)

// errMemberIdle ends the read of a member that stopped sending.
var errMemberIdle = errors.New("the member sent nothing for too long")

// retrying makes a member call, and asks it again when it fails in a way a
// second try can fix.
func (cm *councilMembers) retrying(ctx context.Context, r council.Request, onToken func(string), do func(context.Context) (string, []api.ToolCall, bool, error)) (string, []api.ToolCall, bool, error) {
	wait := councilRetryBackoff
	for attempt := 0; ; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, councilCallTimeout)
		out, calls, cut, err := do(cctx)
		timedOut := cctx.Err() == context.DeadlineExceeded
		cancel()
		if err == nil || ctx.Err() != nil || attempt >= councilRetries || !cm.retryable(err, timedOut) {
			return out, calls, cut, err
		}
		slog.Warn("council: member call failed, asking again", "role", r.Role, "index", r.Index, "attempt", attempt+1, "error", err)
		onToken(fmt.Sprintf("\n\n(%s failed: %v; asked again)\n\n", r.Role, err))
		select {
		case <-ctx.Done():
			return "", nil, false, ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
	}
}

// memberStatus is a member's error with the HTTP status its call ended on.
type memberStatus struct {
	error
	status int
}

func (e memberStatus) Unwrap() error { return e.error }

// retryable reports a failure a second try can fix: a stall, a timeout, a
// dropped connection or a server error. A full owner has its own path, and a
// refusal (4xx other than 429) would be refused again.
func (cm *councilMembers) retryable(err error, timedOut bool) bool {
	var full ownerFullError
	if errors.Is(err, llm.ErrOwnerFull) || errors.As(err, &full) {
		return false
	}
	if timedOut || errors.Is(err, errMemberIdle) {
		return true
	}
	var ms memberStatus
	if errors.As(err, &ms) && ms.status >= 400 && ms.status < 500 && ms.status != http.StatusTooManyRequests {
		return false
	}
	return true
}
