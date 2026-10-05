package server

import (
	"log/slog"
	"sync"

	"github.com/ollama/ollama/llm/engine"
	"github.com/ollama/ollama/types/xollama"
)

// decisionTypeClef is the `decision.type` of a model that carries the Clef
// head (decision/systemone.go). Other decision models are scored from token
// probabilities on /completion, which every engine serves.
const decisionTypeClef = "clef"

// engineHasClefHead is what the pinned engine declares; a variable so a test
// can state either answer.
var engineHasClefHead = engine.HasClefHead

// clefSaid holds the models already named in the log: the launch config is
// built for every request, and the line is worth reading once.
var clefSaid sync.Map

// clefEngine sends a Clef decision model to stock llama-server while the pinned
// opencoti engine has no Clef head: that engine cannot load the model, so every
// /v1/systemone call would end in a 500 (measured on c9, 2026-10-05). An engine
// the model states itself is kept, and once the pin declares clef_score_v1 this
// returns cfg untouched.
func clefEngine(m *Model, cfg *xollama.Config) *xollama.Config {
	if m == nil || m.metadata.String("decision.type") != decisionTypeClef || engineHasClefHead() {
		return cfg
	}
	if cfg != nil && cfg.Engine != "" {
		return cfg
	}
	out := xollama.Config{}
	if cfg != nil {
		out = *cfg
	}
	out.Engine = xollama.EngineLlamaCpp
	if _, said := clefSaid.LoadOrStore(m.ShortName, true); !said {
		slog.Info("Clef decision model is served by stock llama-server: the pinned opencoti engine has no Clef head",
			"model", m.ShortName)
	}
	return &out
}
