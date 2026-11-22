package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
)

// Sending a cloud request through the account pool (plans/cloud-accounts.md
// §3.4). Without a pool both functions are upstream's code path exactly.

// errCloudSigning is a request the primary key could not sign: upstream
// answers it with a sign-in response.
var errCloudSigning = errors.New("cloud proxy signing failed")

// cloudPoolPaths are the inference routes the pool serves. /api/me, usage,
// balance, model details and web search stay on the primary account.
var cloudPoolPaths = map[string]bool{
	"/api/chat": true, "/api/generate": true, "/api/embed": true, "/api/embeddings": true,
	"/v1/chat/completions": true, "/v1/completions": true, "/v1/responses": true,
	"/v1/messages": true, "/v1/embeddings": true, "/v1/systemone": true,
}

// cloudErrorPeek is how much of a failed answer is read to tell why.
const cloudErrorPeek = 64 << 10

// sendCloudRequest signs and sends a cloud passthrough request. With a pool,
// an inference request is signed as an account with a free slot; an answer
// meaning that account ran out (402, "usage limit"), refused its key (401)
// or is busy (429) is sent again on the next account, since nothing has
// reached the client yet. The returned body gives the slot back on Close.
//
// xollama-hook: cloud-accounts (called from proxyCloudRequestWithPath)
func sendCloudRequest(outReq *http.Request, body []byte, path string) (*http.Response, error) {
	if !cloudPoolPaths[path] || !strings.EqualFold(outReq.URL.Hostname(), cloudProxySigningHost) {
		return sendCloudUpstream(outReq)
	}
	ctx := outReq.Context()
	lease, err := theCloudPool.acquire(ctx, nil)
	if err != nil {
		if isCloudExhausted(err) {
			return cloudExhaustedResponse(err), nil
		}
		return nil, err
	}
	if lease == nil {
		return sendCloudUpstream(outReq)
	}
	var tried []*cloudMember
	for {
		tried = append(tried, lease.m)
		req := outReq.Clone(lease.ctx(ctx))
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		var resp *http.Response
		outcome := cloudSignedOut
		if err := cloudProxySignRequest(req.Context(), req); err != nil {
			slog.Warn("cloud proxy signing failed", "account", lease.m.label, "error", err)
			theCloudPool.markSignedOut(lease.m)
		} else {
			if resp, err = http.DefaultClient.Do(req); err != nil {
				lease.release()
				return nil, err
			}
			outcome = cloudPass
			if resp.StatusCode >= 400 {
				peek, _ := io.ReadAll(io.LimitReader(resp.Body, cloudErrorPeek))
				resp.Body = struct {
					io.Reader
					io.Closer
				}{io.MultiReader(bytes.NewReader(peek), resp.Body), resp.Body}
				outcome = classifyCloud(resp.StatusCode, string(peek))
			}
			if outcome == cloudPass || !theCloudPool.handleOutcome(lease.m, outcome) {
				resp.Body = &leasedBody{ReadCloser: resp.Body, lease: lease}
				return resp, nil
			}
		}
		next := nextCloudLease(ctx, lease, tried, outcome)
		if next == nil {
			if resp == nil {
				return nil, errCloudSigning
			}
			return resp, nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		slog.Info("cloud request sent again on another account", "from", tried[len(tried)-1].label, "to", next.m.label)
		lease = next
	}
}

// nextCloudLease gives cur's slot back and takes the account to try next,
// or none. A busy account (429) is replaced only by one with a free slot
// now; one that ran out or refused its key by the next ready account, in
// turn.
func nextCloudLease(ctx context.Context, cur *cloudLease, tried []*cloudMember, o cloudOutcome) *cloudLease {
	cur.release()
	if l := theCloudPool.tryAcquire(tried); l != nil {
		return l
	}
	if o == cloudBusy || !theCloudPool.readyBeyond(tried) {
		return nil
	}
	l, err := theCloudPool.acquire(ctx, tried)
	if err != nil {
		return nil
	}
	return l
}

// sendCloudUpstream is upstream's sign-then-send.
func sendCloudUpstream(outReq *http.Request) (*http.Response, error) {
	if err := cloudProxySignRequest(outReq.Context(), outReq); err != nil {
		slog.Warn("cloud proxy signing failed", "error", err)
		return nil, errCloudSigning
	}
	return http.DefaultClient.Do(outReq)
}

// cloudExhaustedResponse is ollama.com's own answer for an account that ran
// out, for every account at once.
func cloudExhaustedResponse(err error) *http.Response {
	data, _ := json.Marshal(map[string]string{"error": err.Error()})
	return &http.Response{
		StatusCode:    http.StatusPaymentRequired,
		Header:        http.Header{"Content-Type": {"application/json; charset=utf-8"}},
		Body:          io.NopCloser(bytes.NewReader(data)),
		ContentLength: int64(len(data)),
	}
}

// leasedBody gives the slot back when the answer has been read.
type leasedBody struct {
	io.ReadCloser
	lease *cloudLease
}

func (b *leasedBody) Close() error {
	err := b.ReadCloser.Close()
	b.lease.release()
	return err
}

// cloudStubCall runs a pulled cloud model's call (a model with RemoteHost)
// through the pool: as an account with a free slot, and again on the next
// account when ollama.com refuses it before anything was written to the
// client.
//
// xollama-hook: cloud-accounts (called from GenerateHandler and ChatHandler)
func cloudStubCall(c *gin.Context, remote *url.URL, call func(context.Context) error) error {
	ctx := c.Request.Context()
	if remote == nil || !strings.EqualFold(remote.Hostname(), cloudProxySigningHost) {
		return call(ctx)
	}
	lease, err := theCloudPool.acquire(ctx, nil)
	if err != nil {
		if isCloudExhausted(err) {
			return api.StatusError{StatusCode: http.StatusPaymentRequired, ErrorMessage: err.Error()}
		}
		return err
	}
	if lease == nil {
		return call(ctx)
	}
	var tried []*cloudMember
	for {
		tried = append(tried, lease.m)
		err := call(lease.ctx(ctx))
		outcome := cloudPass
		var se api.StatusError
		var ae api.AuthorizationError
		switch {
		case errors.As(err, &se):
			outcome = classifyCloud(se.StatusCode, se.ErrorMessage+" "+se.Status)
		case errors.As(err, &ae):
			outcome = classifyCloud(ae.StatusCode, ae.Status)
		}
		if outcome == cloudPass || c.Writer.Written() || !theCloudPool.handleOutcome(lease.m, outcome) {
			lease.release()
			return err
		}
		next := nextCloudLease(ctx, lease, tried, outcome)
		if next == nil {
			return err
		}
		slog.Info("cloud request sent again on another account", "from", tried[len(tried)-1].label, "to", next.m.label)
		lease = next
	}
}
