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
	// LFM3-VL-2B. Not in c13; upstream v0.40.13 serves LFM3 GGUFs on stock.
	"lfm3": "arch_lfm3_v1",
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
