package api

import (
	"context"
	"encoding/json"
	"net/http"
)

// XollamaMediaPullPath fetches one media component -- a file of a Hugging
// Face repo -- into the server's model store (plans/media-integration.md).
const XollamaMediaPullPath = "/api/xollama/media/pull"

// MediaPullRequest names the file. Digest, when the caller has resolved it
// already, saves the server asking the hub again; the download is verified
// against it either way.
type MediaPullRequest struct {
	// Source is hf.co/<owner>/<repo>/<file>[@rev].
	Source string `json:"source"`
	Digest string `json:"digest,omitempty"`
	Stream *bool  `json:"stream,omitempty"`
}

// MediaPull streams the download's progress; the last response carries the
// digest the file is stored under.
func (c *Client) MediaPull(ctx context.Context, req *MediaPullRequest, fn PullProgressFunc) error {
	return c.stream(ctx, http.MethodPost, XollamaMediaPullPath, req, func(bts []byte) error {
		var resp ProgressResponse
		if err := json.Unmarshal(bts, &resp); err != nil {
			return err
		}
		return fn(resp)
	})
}
