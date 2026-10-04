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

// XollamaMediaVoicesPath lists a speech model's voices: GET with ?model=,
// or POST with a MediaVoicesRequest.
const XollamaMediaVoicesPath = "/api/xollama/media/voices"

// MediaVoicesRequest names the speech model.
type MediaVoicesRequest struct {
	Model string `json:"model"`
}

// Voice is one voice a speech model answers to. Aliases are the names a
// client may ask for instead (OpenAI's alloy, nova, ...), from the template's
// voice_map.
type Voice struct {
	ID      string   `json:"id"`
	Aliases []string `json:"aliases,omitempty"`
}

// MediaVoicesResponse is a speech model's voices: the engine's own, and the
// template's names for them.
type MediaVoicesResponse struct {
	Model string `json:"model"`
	// Default is the voice a request without one gets: the template's
	// default, else the engine's "default" voice when it has one.
	Default string  `json:"default,omitempty"`
	Voices  []Voice `json:"voices"`
	// VoiceMap is the template's map from a client's voice name to the
	// model's.
	VoiceMap map[string]string `json:"voice_map,omitempty"`
	// ResponseFormats and SampleRate are what the engine reports it can
	// answer, when it reports them.
	ResponseFormats []string `json:"response_formats,omitempty"`
	SampleRate      int      `json:"sample_rate,omitempty"`
}

// MediaVoices lists the model's voices. It starts the model's speech engine
// when it is not running yet.
func (c *Client) MediaVoices(ctx context.Context, model string) (*MediaVoicesResponse, error) {
	var resp MediaVoicesResponse
	if err := c.do(ctx, http.MethodPost, XollamaMediaVoicesPath, &MediaVoicesRequest{Model: model}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
