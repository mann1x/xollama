package llm

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A council member marked WithCompactOnFull is refused at once when its
// owner's window is full, with the engine's figures; unmarked, the same
// refusal is waited out as before, and a request over the whole window is
// still ErrNeverFits either way. On eleven2go (a0968aea, council run 4) the
// unmarked wait ran two minutes and failed the turn.
func TestAFullOwnerIsAnsweredAtOnceForACouncilMember(t *testing.T) {
	full := `{"error":{"code":429,"message":"session allocation full (worker of 'manic-hard-council-1790735799': 37121 of 196608 cells free, needs 39662) — compact the session"}}`
	over := `{"error":{"code":429,"message":"session allocation full (worker of 'x': 100 of 8192 cells free, needs 9000)"}}`
	for _, tc := range []struct {
		name, body  string
		marked      bool
		full, never bool
		asked       int32
	}{
		{"member, full owner", full, true, true, false, 1},
		{"plain request, full owner", full, false, false, false, 2},
		{"member, over the window", over, true, false, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var asked atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if asked.Add(1) > 1 {
					w.WriteHeader(http.StatusOK)
					return
				}
				w.Header().Set("Retry-After", "0.01")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			ctx := t.Context()
			if tc.marked {
				ctx = WithCompactOnFull(ctx)
			}
			runner := &llamaServerRunner{client: newLlamaServerHTTPClient(), usedOpencoti: true}
			res, err := runner.postWaitingForAdmission(ctx, srv.URL, []byte(`{}`))
			if res != nil {
				res.Body.Close()
			}
			if errors.Is(err, ErrOwnerFull) != tc.full || errors.Is(err, ErrNeverFits) != tc.never || asked.Load() != tc.asked {
				t.Fatalf("err %v after %d asks: want owner full %v, never fits %v, %d asks", err, asked.Load(), tc.full, tc.never, tc.asked)
			}
			if tc.full && err.Error() != "the session's window is full; compact the session: 37121 of 196608 cells free, needs 39662" {
				t.Errorf("message %q", err)
			}
		})
	}
}
