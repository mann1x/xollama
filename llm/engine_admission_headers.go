package llm

import (
	"context"
	"net/http"
)

// AdmissionHeaders are what the engine tells a client about the pool it was
// admitted to: a saturated pool (--admission-poolless warn), backpressure, a
// settle hold waived and this session's speed. They reach the client through
// the ContextWindow collector, on the same terms as the granted window.
var AdmissionHeaders = []string{"X-KV-Pool-Saturated", "X-Sessions-Remaining", "X-PolyKV-Settle-Waived", "X-Session-TPS"}

// reportAdmissionHeaders records those a response carries. The first report
// wins: a council's members run inside the first admission.
func reportAdmissionHeaders(ctx context.Context, res *http.Response) {
	w, ok := ctx.Value(contextWindowKey{}).(*ContextWindow)
	if !ok || w == nil {
		return
	}
	h := http.Header{}
	for _, name := range AdmissionHeaders {
		if v := res.Header.Get(name); v != "" {
			h.Set(name, v)
		}
	}
	if len(h) > 0 {
		w.admission.CompareAndSwap(nil, &h)
	}
}

// Admission is what the engine reported, nil when it reported nothing.
func (w *ContextWindow) Admission() http.Header {
	if h := w.admission.Load(); h != nil {
		return *h
	}
	return nil
}
