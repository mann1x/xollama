package engine

// GGUF architectures that stock llama.cpp gained before the pinned opencoti
// engine did, each with the pin feature that says the engine serves it.
//
// opencoti is llamafile's llama.cpp plus its own patch chain, so a new
// architecture reaches stock llama-server first -- every upstream sync moves
// LLAMA_CPP_VERSION -- and opencoti only once it is ported. While the pin does
// not declare a row's feature, a model of that architecture is served by stock
// llama-server (archEngine, server/arch_engine.go). The engine claims the
// architecture by declaring the feature; nothing here changes when it does.
//
// A row with no feature records an architecture llama.cpp added that the
// pinned engine was checked to serve already.
//
// scripts/check-engine-archs.sh fails a move of LLAMA_CPP_VERSION that adds an
// architecture with no row here; keep one row per line, `"<arch>": "<feature>",`.
var archFeatures = map[string]string{
	// llama.cpp #31252 (b11963, 2026-10-27): LiquidAI LFM3-1.7B and
	// LFM3-VL-2B. opencoti serves it since c14.
	"lfm3": "arch_lfm3_v1",
	// llama.cpp b12111 (upstream v0.40.15) adds four; the c14 r9 engine had
	// none of them, opencoti serves all four since c14 r10.
	// #29433 (2026-10-29): Limite 1B.
	"limite": "arch_limite_v1",
	// #31306 (2026-11-02): Clover MoE.
	"clover_moe": "arch_clover_moe_v1",
	// #31134 (2026-11-03): Metis-1.6.
	"metis16": "arch_metis16_v1",
	// #31376 (2026-10-29): Mistral Large 4.
	"mistral-large4": "arch_mistral_large4_v1",
	// EmbeddingGemma 2 (embeddinggemma-2:270m), in llama.cpp since before
	// b11803, so older than this table and its sync check; every engine up to
	// c14 r10 refused it, opencoti serves it since c14 r11.
	"gemma-embedding2": "arch_gemma_embedding2_v1",
	// llama.cpp b12177 (upstream v0.40.16) adds two; the c14 r15 engine had
	// neither, opencoti serves both since c14 r16.
	// #31692 (2026-11-05): Ripple by ricursiveai, the model upstream's MLX
	// runner, renderer and parser also gained in v0.40.16 (#19592).
	"ricursive": "arch_ricursive_v1",
	// #31589 (2026-11-06): xing4_0.
	"xing4_0": "arch_xing4_0_v1",
	// llama.cpp b12257 (upstream v0.40.17) adds one; c14 r18 does not have it.
	// #31779 (2026-11-06): Kimi K3.1 Pro, text only.
	"kimi-k31": "arch_kimi_k31_v1",
	// llama.cpp b12651 (upstream v0.40.18) adds eight; c14 r20 has none.
	// #32217 (2026-11-15): Inkling 2, text and vision.
	"inkling2": "arch_inkling2_v1",
	// #32694 (2026-11-20): Inkling 2 Mini.
	"inkling2_loop": "arch_inkling2_loop_v1",
	// #32559 (2026-11-20): Domyn Edge 4B.
	"domynedge": "arch_domynedge_v1",
	// #32071 (2026-11-18): Zyphra Zaya1 8B.
	"zaya1": "arch_zaya1_v1",
	// #32413 (2026-11-17): Kokoro TTS (mtmd).
	"kokoro": "arch_kokoro_v1",
	// #32427 (2026-11-18): tiny-voca (mtmd).
	"tiny-voca": "arch_tiny_voca_v1",
	// #32020 (2026-11-15): Zonos3-mini TTS (mtmd).
	"zonos3": "arch_zonos3_v1",
	// #32491 (2026-11-21): kyutai Hibiki full-duplex speech (mtmd).
	"moshi": "arch_moshi_v1",
}

// ArchFeature is the pin feature that says the engine serves GGUF architecture
// arch, empty when the table names none.
func ArchFeature(arch string) string {
	return archFeatures[arch]
}

// ServesArch reports whether the engine this build ships serves GGUF
// architecture arch. An architecture the table does not gate is the engine's
// to serve or refuse.
func ServesArch(arch string) bool {
	f := archFeatures[arch]
	if f == "" {
		return true
	}
	pin, err := loadPin()
	if err != nil {
		return false
	}
	return pin.HasFeature(f)
}
