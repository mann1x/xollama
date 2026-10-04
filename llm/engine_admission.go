package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
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

// errNoAdmission is the engine refusing for longer than admissionRetryBudget.
// It is a busy server, not a crashed one, and callers say so.
var errNoAdmission = errors.New("the engine has had no room for this request for " +
	admissionRetryBudget.String() + "; it is refusing new work rather than queueing it, " +
	"which usually means the context or the slot ceiling is too large for the memory available")

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
			reportEngineWindow(ctx, res)
			return res, nil
		}

		wait := retryAfter(res, time.Now())
		// The body is not the caller's to see -- this response is being
		// swallowed -- but it has to be drained so the connection is reusable.
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		// A client negotiating its own window, or driving its own pool tree,
		// gets the engine's answer now -- a request that can never fit
		// included: that client can grow its owner.
		if Negotiating(ctx) {
			return nil, refuseWindow(ctx, body, wait)
		}
		if err := neverFits(body); err != nil {
			return nil, err
		}
		if CompactsOnFull(ctx) {
			if err := ownerFull(body); err != nil {
				return nil, err
			}
		}

		if time.Now().Add(wait).After(deadline) {
			return nil, errNoAdmission
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

// sessionFull is the engine's refusal of a request inside a session's window:
// "session allocation full (worker of 'x': 15229 of 16384 cells free, needs
// 15398)" or "session allocation full ('x': …)".
var sessionFull = regexp.MustCompile(`session allocation full \([^)]*?(\d+) of (\d+) cells free, needs (\d+)`)

// ErrNeverFits is a request that needs more cells than its whole window: no
// wait can seat it.
var ErrNeverFits = errors.New("the request needs more context than its window holds")

// neverFits reads an admission refusal and reports one that no wait can
// cure: a request needing more cells than its session's whole window.
// Measured on b137: such a request was refused 52 times over 2 minutes before
// errNoAdmission, when the first refusal already said it could never fit.
// Anything else -- cells held by others, which they give back -- is waited
// out as before.
func neverFits(body []byte) error {
	m := sessionFull.FindSubmatch(body)
	if m == nil {
		return nil
	}
	// The pattern admits digits only, so these parse; a number too large for
	// an int reads as 0 and never refuses.
	total, _ := strconv.Atoi(string(m[2]))
	need, _ := strconv.Atoi(string(m[3]))
	if total <= 0 || need <= total {
		return nil
	}
	return fmt.Errorf("%w: it needs %d cells of a %d-cell window; shorten the conversation or raise num_ctx", ErrNeverFits, need, total)
}

// ErrOwnerFull is a council member refused because its owner's window is full
// ("session allocation full ... -- compact the session"): the request fits the
// window, but the conversation and the turn's layers hold the rest. Waiting
// cures it only while another member of the turn holds cells; the council
// knows when none does, and compacts (server/council_owner_full.go).
var ErrOwnerFull = errors.New("the session's window is full; compact the session")

type compactOnFullKey struct{}

// WithCompactOnFull marks a council member's request: a refusal for a full
// owner is answered at once with ErrOwnerFull instead of being waited out.
func WithCompactOnFull(ctx context.Context) context.Context {
	return context.WithValue(ctx, compactOnFullKey{}, true)
}

// CompactsOnFull reports whether ctx is marked by WithCompactOnFull.
func CompactsOnFull(ctx context.Context) bool {
	v, _ := ctx.Value(compactOnFullKey{}).(bool)
	return v
}

// ownerFull reads an admission refusal for a full session window that the
// request would fit (neverFits has the other kind).
func ownerFull(body []byte) error {
	m := sessionFull.FindSubmatch(body)
	if m == nil {
		return nil
	}
	free, _ := strconv.Atoi(string(m[1]))
	total, _ := strconv.Atoi(string(m[2]))
	need, _ := strconv.Atoi(string(m[3]))
	if total <= 0 || need > total {
		return nil
	}
	return fmt.Errorf("%w: %d of %d cells free, needs %d", ErrOwnerFull, free, total, need)
}
