package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ContextWindowHeader is opencoti's report of the context window a request
// was guaranteed: on every admitted response, streaming included, when the
// engine books windows (elastic admission); absent means no guarantee. A
// continuation of a live session reports the window it already holds. xollama
// passes it on to its own clients unchanged (docs/xollama/sessions.mdx).
const ContextWindowHeader = "X-Context-Window"

// LargestAdmissibleHeader is opencoti's answer to a window it cannot book: the
// largest one it would admit right now. xollama sends it, with Retry-After,
// on the 429 a negotiating client gets (see WithNegotiation).
const LargestAdmissibleHeader = "X-Context-Largest-Admissible"

// ContextWindow collects the window the engine granted a request, for the
// handler to report. The first report wins: a council's members, a
// structured-output second pass and the like run inside the window the first
// admission of the request booked.
type ContextWindow struct {
	n atomic.Int64
	// largest and retry are a negotiating request's refusal.
	largest, retry atomic.Int64
}

type contextWindowKey struct{}

// WithContextWindow returns ctx carrying a fresh ContextWindow.
func WithContextWindow(ctx context.Context) (context.Context, *ContextWindow) {
	w := &ContextWindow{}
	return context.WithValue(ctx, contextWindowKey{}, w), w
}

// Get is the granted window, or 0 when none was reported.
func (w *ContextWindow) Get() int { return int(w.n.Load()) }

// Refusal is the engine's refusal of a negotiating request: the largest
// window it would admit now (0 when it did not say), and when to ask again.
// ok is false when the request was not refused.
func (w *ContextWindow) Refusal() (largest int, retryAfter time.Duration, ok bool) {
	r := w.retry.Load()
	return int(w.largest.Load()), time.Duration(r), r > 0
}

type negotiationKey struct{}

// WithNegotiation marks a request whose client states its own window
// (placement.num_ctx): an engine that cannot book it answers at once with a
// 429 and the largest window it would admit, instead of the request waiting
// for room as ollama's queue would. The client chooses: retry smaller, or
// later.
func WithNegotiation(ctx context.Context) context.Context {
	return context.WithValue(ctx, negotiationKey{}, true)
}

// Negotiating reports whether ctx is marked by WithNegotiation.
func Negotiating(ctx context.Context) bool {
	v, _ := ctx.Value(negotiationKey{}).(bool)
	return v
}

// WindowRefusedError is a negotiating request the engine could not admit.
type WindowRefusedError struct {
	LargestAdmissible int
	Message           string
}

func (e *WindowRefusedError) Error() string { return e.Message }

// refuseWindow records the refusal for the handler and returns its error.
func refuseWindow(ctx context.Context, body []byte, wait time.Duration) error {
	largest := largestAdmissible(body)
	ReportWindowRefusal(ctx, largest, wait)
	return &WindowRefusedError{LargestAdmissible: largest, Message: refusalMessage(body)}
}

// refusalMessage is the engine's own reason, from its OpenAI-shaped error
// body, or the body itself when it is not one.
func refusalMessage(body []byte) string {
	var r struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &r) == nil && r.Error.Message != "" {
		return r.Error.Message
	}
	return strings.TrimSpace(string(body))
}

// ReportWindowRefusal records a negotiating request's refusal for the
// handler: the largest window the engine would admit, and when to ask again
// (at least a second).
func ReportWindowRefusal(ctx context.Context, largest int, retryAfter time.Duration) {
	if w, ok := ctx.Value(contextWindowKey{}).(*ContextWindow); ok {
		w.largest.Store(int64(max(largest, 0)))
		w.retry.Store(int64(max(retryAfter, time.Second)))
	}
}

// largestAdmissible reads largest_admissible from a refusal, at the top level
// or inside "error", as the engine sends it.
func largestAdmissible(body []byte) int {
	var r struct {
		LargestAdmissible int `json:"largest_admissible"`
		Error             struct {
			LargestAdmissible int `json:"largest_admissible"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &r) != nil {
		return 0
	}
	return max(r.LargestAdmissible, r.Error.LargestAdmissible, 0)
}

// ReportContextWindow records n as the request's window, unless one is
// already recorded or ctx carries no ContextWindow.
func ReportContextWindow(ctx context.Context, n int) {
	if w, ok := ctx.Value(contextWindowKey{}).(*ContextWindow); ok && n > 0 {
		w.n.CompareAndSwap(0, int64(n))
	}
}

// reportEngineWindow records the window an engine response states. Stock
// llama.cpp never states one, so nothing is reported there.
func reportEngineWindow(ctx context.Context, res *http.Response) {
	if res.StatusCode >= http.StatusBadRequest {
		return
	}
	if n, err := strconv.Atoi(strings.TrimSpace(res.Header.Get(ContextWindowHeader))); err == nil {
		ReportContextWindow(ctx, n)
	}
}
