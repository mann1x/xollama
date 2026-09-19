package llm

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// xollama-hook: launch-config
//
// Turning the engine's backpressure back into queueing.
//
// With dynamic slots the engine owns admission: it parks slots and admits them
// as the decode rate and free VRAM allow, and it refuses a request it cannot
// seat with 429 and a Retry-After, before any prefill. That is the right
// behaviour for the engine and the wrong behaviour to hand a caller, because
// ollama's contract is that a busy server queues. A client that has never heard
// of any of this must not start seeing 429s because a server-side default
// changed.
//
// So a 429 is waited out here instead of being returned. Only on the engine
// that has an admission gate, and only for as long as the caller's own context
// allows -- a caller who gives up is still the one who decides.

// admissionRetryBudget caps how long a single request will wait to be seated.
//
// It exists so a permanently saturated server fails visibly rather than hanging
// for as long as a client is willing to wait. The caller's context still wins
// when it is shorter, which it usually is.
const admissionRetryBudget = 2 * time.Minute

// admissionRetryFallback is how long to wait when the engine refuses without
// saying for how long.
const admissionRetryFallback = 250 * time.Millisecond

// retryAfter reads the header, which may be seconds or an HTTP date. A value
// that is missing or unparseable is not an error: the engine is allowed to
// refuse without advice, and the fallback covers it.
func retryAfter(res *http.Response, now time.Time) time.Duration {
	v := res.Header.Get("Retry-After")
	if v == "" {
		return admissionRetryFallback
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		if secs <= 0 {
			return admissionRetryFallback
		}
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
		return admissionRetryFallback
	}
	return admissionRetryFallback
}

// postWaitingForAdmission posts payload, waiting out an engine that says it has
// no room yet.
//
// payload is kept rather than a reader, because a retry needs to send the body
// again and a consumed reader cannot.
func (s *llamaServerRunner) postWaitingForAdmission(ctx context.Context, endpoint string, payload []byte) (*http.Response, error) {
	deadline := time.Now().Add(admissionRetryBudget)
	attempt := 0

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")

		res, err := s.httpClient().Do(req)
		if err != nil {
			return nil, err
		}

		// Everything except an admission refusal on an engine that has an
		// admission gate is the caller's answer, untouched.
		if res.StatusCode != http.StatusTooManyRequests || !s.usedOpencoti {
			return res, nil
		}

		wait := retryAfter(res, time.Now())
		// The body is not the caller's to see -- this response is being
		// swallowed -- but it has to be drained so the connection is reusable.
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()

		if time.Now().Add(wait).After(deadline) {
			return nil, errors.New("the engine has had no room for this request for " +
				admissionRetryBudget.String() + "; it is refusing new work rather than queueing it, " +
				"which usually means the context or the slot ceiling is too large for the memory available")
		}

		attempt++
		slog.Debug("engine has no room yet, waiting", "attempt", attempt, "wait", wait)

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
