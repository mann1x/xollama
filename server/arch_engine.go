package server

import (
	"log/slog"
	"runtime"
	"sync"

	"github.com/ollama/ollama/llm/engine"
	"github.com/ollama/ollama/types/xollama"
)

// engineServesArch is what the pinned engine declares; a variable so a test
// can state either answer.
var engineServesArch = engine.ServesArch

// archGOOS is the platform archEngine decides for; a variable so a test can
// ask about macOS on any runner.
var archGOOS = runtime.GOOS

// archSaid holds the models already named in the log: the launch config is
// built for every request, and the line is worth reading once.
var archSaid sync.Map

// archEngine sends a model whose GGUF architecture the pinned opencoti engine
// does not serve to stock llama-server. Stock llama.cpp gains an architecture
// first and opencoti once it is ported; llm/engine/arch.go lists each such
// architecture with the pin feature that ends the detour (lfm3: c13 has none,
// upstream v0.40.13 serves LFM3 GGUFs on llama.cpp b11963). An engine the
// model states itself is kept. Where no stock llama-server ships (macOS) the
// load stays on opencoti, which names the architecture it lacks; moving it
// would only trade that for the generic stockless refusal.
func archEngine(m *Model, cfg *xollama.Config) *xollama.Config {
	if m == nil || !engine.StockShipped(archGOOS) {
		return cfg
	}
	arch := m.metadata.String("general.architecture")
	if arch == "" || engineServesArch(arch) {
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
	if _, said := archSaid.LoadOrStore(m.ShortName, true); !said {
		slog.Warn("model is served by stock llama-server: the pinned opencoti engine does not serve its architecture",
			"model", m.ShortName, "architecture", arch, "until_engine_declares", engine.ArchFeature(arch))
	}
	return &out
}
