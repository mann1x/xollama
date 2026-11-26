package server

import (
	"log/slog"
	"time"

	"github.com/ollama/ollama/api"
)

// modelKeepAlive is how long a runner stays loaded after its last request
// (keep-alive hook, called from Scheduler.load).
//
// Precedence, most specific first: the request's own keep_alive, then the
// model's (`xollama tweak model --keep-alive`), then the server's
// OLLAMA_KEEP_ALIVE (def). A model that states nothing keeps upstream's
// answer exactly: off means off.
//
// It is read at load only, as upstream reads the request's: a request that
// names no keep_alive leaves a loaded runner's duration as it is.
func modelKeepAlive(m *Model, requested *api.Duration, def time.Duration) time.Duration {
	if requested != nil {
		return requested.Duration
	}
	if m == nil {
		return def
	}
	if d, ok := m.Xollama.KeepAliveDuration(); ok {
		slog.Debug("keep-alive from the model's config", "model", m.ShortName, "keep_alive", m.Xollama.KeepAlive)
		return d
	}
	return def
}
