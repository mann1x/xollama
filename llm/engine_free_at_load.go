package llm

import "github.com/ollama/ollama/envconfig"

// recordFreeAtLoad keeps what a device had free when this engine process
// started allocating: the first "using device <dev> (...) - N MiB free" line
// of the process.
//
// A load with an assistant drafter opens a second model in the same process,
// and llama.cpp prints the line again for it, after the target's weights,
// KV and compute are already on the card (opencoti c13, RTX 3090: 22089 MiB
// before the target, 6875 MiB before the head). Upstream keeps the last line,
// and GetDeviceInfos then takes "free at load minus what this runner uses":
// 6875 MiB minus about 15 GiB floors at 0. That 0 is written into
// discovery's device table, the card reads as full, and every load after it
// went to another GPU (eleven2go, 2026-11-16, mails #2309 and #2310: CUDA0
// free 0 while nvidia-smi showed 24 GB free, the next loads on the 9070 XT).
//
// XOLLAMA_ENGINE=llamacpp keeps upstream's last-line rule (CLAUDE.md rule 2).
func recordFreeAtLoad(freeAtLoad map[string]uint64, dev string, free uint64) {
	if _, seen := freeAtLoad[dev]; seen && envconfig.Engine() != "llamacpp" {
		return
	}
	freeAtLoad[dev] = free
}
