package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/ollama/ollama/types/xollama"
)

// A model with an LLM and media runs both in one opencoti process, so its media
// answers while the LLM is loaded and takes no place of its own. The engine runs
// a media job in a fiber on the decode loop, and the LLM's texts stay
// byte-identical beside it (opencoti M7.6, gate b103). The window pays for the
// media's memory, so the load is priced with it.

// CombinedMedia is what a load adds for a model's media: its engine arguments
// and the memory they take on the GPU. Media that stays on the CPU books none.
func CombinedMedia(m *xollama.Media, path func(string) string, size func(string) int64) ([]string, uint64) {
	if m.IsZero() {
		return nil, 0
	}
	if mediaOnCPU(m, path) {
		return MediaArgs(m, path), 0
	}
	return MediaArgs(m, path), MediaEstimate(m, size)
}

// mediaArgsFor adds the model's media to a launch on opencoti. Stock llama.cpp
// has no such flags; a model that lands there has its media in a process of its
// own (server/media_combined.go).
func mediaArgsFor(args []string, cfg LlamaServerConfig, usedOpencoti bool) []string {
	if !usedOpencoti {
		return args
	}
	return append(args, cfg.MediaArgs...)
}

// MediaDo sends one request to the media surface of an engine serving an LLM.
func (s *llamaServerRunner) MediaDo(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string) (*http.Response, error) {
	if s.port == 0 {
		return nil, errors.New("engine is not listening yet")
	}
	u := url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", s.port), Path: path, RawQuery: query.Encode()}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return http.DefaultClient.Do(req)
}

// MediaFeatures is what the engine's /health advertises now, nil when it cannot say.
func (s *llamaServerRunner) MediaFeatures() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := s.MediaDo(ctx, http.MethodGet, "/health", nil, nil, "")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var h mediaHealth
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&h) != nil {
		return nil
	}
	return h.Features
}

// CheckMediaFeatures refuses by name what the running engine lacks for m.
func CheckMediaFeatures(m *xollama.Media, have []string) error {
	if missing := missingFeatures(MediaFeatures(m), have); len(missing) > 0 {
		return refusal(missing)
	}
	return nil
}
