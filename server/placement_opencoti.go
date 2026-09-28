package server

import (
	"log/slog"

	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/ml"
)

// opencotiPlacement keeps a model that only opencoti can serve on the GPUs
// opencoti serves (the opencoti-placement hook). Upstream's placement picks one
// backend by free memory; on eleven2go, with an RTX 3090 (CUDA, opencoti) and an
// RX 9070 XT (Vulkan, stock llama.cpp on this pin), it picked Vulkan for a
// kvarn3 council model and the launch refused it.
//
// It changes nothing for a model stock llama.cpp can serve, nor when no GPU
// group would run on opencoti -- the launch's own refusal then names the setting
// -- nor with XOLLAMA_ENGINE=llamacpp, where no group does.
func opencotiPlacement(cfg llm.LlamaServerConfig, gpus []ml.DeviceInfo) []ml.DeviceInfo {
	why := llm.NeedsOpencoti(cfg)
	if why == "" || len(gpus) < 2 {
		return gpus
	}
	var kept []ml.DeviceInfo
	for _, group := range ml.ByLibrary(gpus) {
		if llm.WouldUseOpencoti(cfg, group) {
			kept = append(kept, group...)
		}
	}
	if len(kept) == 0 || len(kept) == len(gpus) {
		return gpus
	}
	slog.Info("placing the model only on GPUs opencoti serves", "reason", why, "gpus", len(kept), "of", len(gpus))
	return kept
}
