package server

import (
	"context"
	"log/slog"
	"os"
	"sync"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/types/model"
)

// combinedMedia reports whether m's media runs in its LLM's process. Media alone
// has no LLM to share; an MLX model is not served by opencoti; an embedding or
// decision model does not generate.
func combinedMedia(m *Model) bool {
	return modelMedia(m) != nil && m.ModelPath != "" && !isMediaKey(m.ModelPath) && m.isGGUF() &&
		!m.metadata.Valid("pooling_type") && m.metadata.String("decision.type") == ""
}

// mediaBlobsSaid holds the models already named in the log: the launch config
// is built for every request.
var mediaBlobsSaid sync.Map

// combinedMediaLaunch is the launch's media. A missing component keeps the LLM
// serving: its media goes the separate way, which refuses it by name.
//
// xollama-hook: media (called from llamaServerConfigForModel)
func combinedMediaLaunch(m *Model) ([]string, uint64) {
	if !combinedMedia(m) {
		return nil, 0
	}
	media := modelMedia(m)
	for _, c := range media.Components() {
		if _, err := os.Stat(blobPath(c.Digest)); err != nil {
			if _, said := mediaBlobsSaid.LoadOrStore(m.ShortName, true); !said {
				slog.Warn("media component missing; the LLM loads without its media", "model", m.ShortName, "component", c.Name, "digest", c.Digest)
			}
			return nil, 0
		}
	}
	return llm.CombinedMedia(media, blobPath, blobSize)
}

type mediaCallKey struct{}

// isMediaCall says a request only needs the model's engine, with no options of
// its own: a running LLM serves it whatever a chat loaded it with, where
// comparing options would reload the model under that chat.
func isMediaCall(ctx context.Context) bool {
	return ctx != nil && ctx.Value(mediaCallKey{}) != nil
}

// combinedMediaRunner schedules m itself and returns its engine, or false when
// m's media does not run in its LLM's process: a model stock llama.cpp serves
// keeps a media engine of its own.
func (s *Server) combinedMediaRunner(ctx context.Context, m *Model, keepAlive *api.Duration) (llm.MediaRunner, bool, error) {
	cfg := llamaServerConfigForModel(m)
	if len(cfg.MediaArgs) == 0 || !llm.WouldUseOpencoti(cfg, nil) {
		return nil, false, nil
	}
	r, _, _, err := s.scheduleRunnerForModel(context.WithValue(ctx, mediaCallKey{}, true), m, []model.Capability{}, nil, keepAlive, nil)
	if err != nil {
		return nil, true, err
	}
	mr, ok := r.(llm.MediaRunner)
	if !ok {
		return nil, false, nil
	}
	if err := llm.CheckMediaFeatures(modelMedia(m), mr.MediaFeatures()); err != nil {
		return nil, true, err
	}
	return mr, true, nil
}
