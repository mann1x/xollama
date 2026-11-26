package server

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/types/model"
)

// preloadNames reads XOLLAMA_PRELOAD_MODELS: names separated by commas,
// blanks dropped, each once, in the order given.
func preloadNames(list string) []string {
	var out []string
	for _, n := range strings.Split(list, ",") {
		n = strings.TrimSpace(n)
		if n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// preloadPlan says how a model is loaded ahead of its first request: by its
// text runner (which also starts a GGUF model's own media, in one process),
// or by its media engine through one of the kinds it serves (one engine
// serves them all). skip names why there is nothing to load.
func preloadPlan(m *Model) (text bool, kind model.Capability, skip string) {
	if m.Config.RemoteHost != "" && m.Config.RemoteModel != "" {
		return false, "", "a cloud model runs on ollama.com; there is nothing to load here"
	}
	caps := m.Capabilities()
	if slices.Contains(caps, model.CapabilityCompletion) || slices.Contains(caps, model.CapabilityEmbedding) {
		return true, "", ""
	}
	if media := mediaCapabilities(m); len(media) > 0 {
		return false, media[0], ""
	}
	return false, "", "it neither generates nor serves media"
}

// preloadModels loads the models XOLLAMA_PRELOAD_MODELS names, one at a time,
// once the server has its GPUs (preload hook, called from Serve).
//
// It is an ordinary load: the request ends as soon as the model is loaded, so
// the runner's idle timer starts at once, and how long it then stays is the
// model's own keep_alive, else OLLAMA_KEEP_ALIVE (modelKeepAlive). A
// preloaded model unloads like any other (xollama stop, keep_alive 0) and the
// scheduler may evict it. A model that cannot be loaded is said at Warn; the
// server serves on regardless.
func (s *Server) preloadModels(ctx context.Context, list string) {
	names := preloadNames(list)
	if len(names) == 0 {
		return
	}
	slog.Info("preloading models", "models", strings.Join(names, ","))
	for _, name := range names {
		if ctx.Err() != nil {
			return
		}
		if err := s.preloadModel(ctx, name); err != nil {
			slog.Warn("preload: model not loaded", "model", name, "error", err)
		}
	}
}

func (s *Server) preloadModel(ctx context.Context, name string) error {
	m, err := GetModel(name)
	if err != nil {
		return err
	}
	text, kind, skip := preloadPlan(m)
	if skip != "" {
		return fmt.Errorf("%s", skip)
	}
	// The load's request ends here, whatever happens: a held context would
	// keep the runner busy and its keep-alive would never start.
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if text {
		_, _, _, err = s.scheduleRunner(lctx, name, "", nil, nil, nil, nil)
	} else {
		_, _, err = s.mediaRunner(lctx, name, kind, nil)
	}
	if err != nil {
		return err
	}
	keepAlive := ""
	if m.Xollama != nil {
		keepAlive = m.Xollama.KeepAlive
	}
	if keepAlive == "" {
		keepAlive = envconfig.KeepAlive().String() + " (OLLAMA_KEEP_ALIVE; set the model's own with xollama tweak model --keep-alive)"
	}
	slog.Info("preloaded model", "model", name, "keep_alive", keepAlive)
	return nil
}
