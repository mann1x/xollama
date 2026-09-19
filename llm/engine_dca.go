package llm

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/fs/gguf"
	"github.com/ollama/ollama/ml"
)

// xollama-hook: launch-config
//
// Dual Chunk Attention: running a model past the context it was trained on.
//
// A model has a window it was trained in, and ollama enforces it -- num_ctx is
// clamped to the GGUF's context_length with a warning, and that is the end of
// the conversation. It is the right default, because attention over distances
// the model never saw during training degrades badly and silently.
//
// DCA is the training-free way out. It routes the full-attention layers through
// chunked positions, so no query-key distance ever exceeds the pretrain window
// even when the sequence does: positions inside a chunk are native, and
// distances between chunks are clamped back into the window. The model is
// therefore never asked a question it has not been trained to answer.
//
// Three flags go together, and none of them works alone:
//
//	--dca on                                     the chunked route itself
//	--rope-scaling yarn                          the positional stretch
//	--override-kv <arch>.context_length=int:N    what the engine believes
//
// The last one is the reason this is not just a flag. The engine reads the
// model's own trained context out of the GGUF and sizes everything from it, so
// without the override it would refuse the longer context for the same reason
// ollama does. xollama writes all three, and only together.

// dcaPlan is the resolved DCA setting for one load.
type dcaPlan struct {
	// Enabled is the answer after every plane has had its say.
	Enabled bool
	// ChunkSize is the chunk length in tokens. Zero means auto, which is the
	// model's own pretrain window and is the right answer almost always.
	ChunkSize int
}

// dcaArchitectures are the architectures whose graph actually has the chunked
// attention route. This list is not decoration.
//
// On anything else --dca is accepted and then does nothing at all: the flag
// parses, the server starts, and the attention graph never calls the DCA path,
// so a load that looks configured for long context is running unprotected past
// its trained window. That is precisely the silent degradation this fork exists
// to refuse, so the list is checked here rather than trusted to the engine.
//
// Source of truth is which models in the engine build a DCA input --
// src/models/{gemma4,gemma4-assistant,qwen2,qwen3,qwen3moe,qwen35,qwen35moe}.cpp
// -- mapped through llama-arch.cpp to the strings a GGUF actually carries.
var dcaArchitectures = []string{
	"gemma4", "gemma4-assistant",
	"qwen2", "qwen3", "qwen3moe", "qwen35", "qwen35moe",
}

// archHasDCARoute reports whether this architecture's attention graph has the
// chunked route at all.
func archHasDCARoute(arch string) bool {
	return slices.Contains(dcaArchitectures, arch)
}

// resolveDCAPlan applies the precedence the documentation promises: the model's
// own xollama.json, then the environment, then off.
//
// There is no request field and no command-line switch, deliberately. DCA
// changes how the runner is launched, so it cannot vary between two requests
// that share one -- asking for it per request would silently mean "start me a
// second copy of this model", which is not what anyone typing it would expect.
// The context length is already the per-request knob, and it is the one that
// matters.
func resolveDCAPlan(cfg LlamaServerConfig) dcaPlan {
	plan := dcaPlan{
		Enabled:   envconfig.DCA(),
		ChunkSize: int(envconfig.DCAChunkSize()),
	}

	if cfg.Xollama != nil && cfg.Xollama.DCA != nil {
		d := cfg.Xollama.DCA
		if d.Enabled != nil {
			plan.Enabled = *d.Enabled
		}
		if d.ChunkSize > 0 {
			plan.ChunkSize = d.ChunkSize
		}
	}

	if !plan.Enabled {
		return dcaPlan{}
	}
	return plan
}

// requiresEngineExtension names the reason this plan needs opencoti, or "".
func (p dcaPlan) requiresEngineExtension() string {
	if !p.Enabled {
		return ""
	}
	return "dual chunk attention (--dca) is an engine extension; stock llama.cpp has no such flag"
}

// checkDCAArchitecture refuses, or warns about, a model whose graph has no
// chunked route.
//
// The two answers are different because the stakes are. Below the trained
// window DCA is an optional route and its absence costs nothing, so a load that
// merely asked for it gets a warning. Above the trained window DCA is the only
// thing holding the model together, and starting anyway would serve confident
// nonsense past the native context -- so that is refused, with the number that
// would have to change to make it start.
func checkDCAArchitecture(arch string, numCtx, trainCtx int) (warn string, refuse error) {
	if archHasDCARoute(arch) {
		return "", nil
	}
	if trainCtx > 0 && numCtx > trainCtx {
		return "", fmt.Errorf(
			"dual chunk attention has no route on the %q architecture, so a context of %d cannot be served safely past this model's trained %d; lower num_ctx to %d or below, or turn DCA off",
			arch, numCtx, trainCtx, trainCtx)
	}
	return fmt.Sprintf(
		"dual chunk attention has no route on the %q architecture and will do nothing for this model", arch), nil
}

// resolveDCAChunk returns the chunk length to pass, and refuses a length the
// engine would throw on.
//
// Passing one explicitly is not optional on a past-native load, and the reason
// is a trap worth spelling out. --dca-chunk-size 0 means "auto", and auto
// resolves to the model's original context -- which the engine reads from
// n_ctx_train, which is precisely the value the metadata override rewrites.
// Leave it on auto and the chunk becomes the whole requested context: one
// chunk, no inter-chunk distances, DCA doing nothing at all, while every log
// line still says --dca on. Naive extension wearing the flag's name.
//
// So a past-native load derives the chunk from the context the model was
// actually trained in, read before the override changes the engine's mind
// about what that is.
//
// The engine requires chunk % n_ubatch == 0 and throws at context creation
// otherwise. A derived chunk is rounded down to satisfy that -- a shorter chunk
// keeps more distances inside the trained window, so it errs safe. A chunk the
// user asked for is refused instead, because silently serving a different
// number than the one in someone's config is how a setting stops meaning
// anything.
func resolveDCAChunk(plan dcaPlan, numCtx, trainCtx, numBatch int) (int, error) {
	pastNative := trainCtx > 0 && numCtx > trainCtx

	chunk, derived := plan.ChunkSize, false
	if chunk <= 0 {
		if !pastNative {
			// Within the trained window auto is correct: it resolves to the
			// model's own pretrain context, which nothing has rewritten.
			return 0, nil
		}
		chunk, derived = trainCtx, true
	}

	if numBatch > 0 && chunk%numBatch != 0 {
		if !derived {
			return 0, fmt.Errorf(
				"dca.chunk_size / XOLLAMA_DCA_CHUNK_SIZE = %d must be a multiple of the batch size %d, or the engine refuses the context at startup; use %d, or set num_batch to a divisor of %d",
				chunk, numBatch, chunk/numBatch*numBatch, chunk)
		}
		chunk = chunk / numBatch * numBatch
		if chunk <= 0 {
			return 0, fmt.Errorf(
				"dual chunk attention needs a chunk of at least the batch size %d, but this model was trained in only %d tokens; lower num_batch, or turn DCA off",
				numBatch, trainCtx)
		}
	}
	return chunk, nil
}

// appendDCAArgs writes the DCA arguments once the engine is known.
//
// The metadata override is written only when the requested context is actually
// past the model's own. Below that the engine already believes the right thing
// and rewriting it would change nothing except what auto-chunk resolves to.
//
// --rope-scaling yarn is deliberately NOT written. Without --yarn-orig-ctx or
// --rope-scale it has nothing to scale against, and the engine's own validated
// long-context runs do not use it: RULER-VT 0.984 at 256k is --dca on with an
// explicit chunk and no YaRN. Composing the two is a real thing the engine can
// do, through --dca-yarn-factor, but there is no measured recipe to copy yet.
func appendDCAArgs(args []string, plan dcaPlan, arch string, numCtx, trainCtx, numBatch int, usedOpencoti bool) ([]string, error) {
	if !plan.Enabled || !usedOpencoti {
		return args, nil
	}

	chunk, err := resolveDCAChunk(plan, numCtx, trainCtx, numBatch)
	if err != nil {
		return nil, err
	}

	args = append(args, "--dca", "on")
	if chunk > 0 {
		args = append(args, "--dca-chunk-size", strconv.Itoa(chunk))
	}
	if trainCtx > 0 && numCtx > trainCtx && arch != "" {
		args = append(args, "--override-kv", fmt.Sprintf("%s.context_length=int:%d", arch, numCtx))
	}
	return args, nil
}

// DCAUnlocksContext reports whether this load may be given more context than the
// model was trained with.
//
// It is the one question the scheduler needs answered, and it has to be
// answered the same way in three places: the clamp that would otherwise cut
// num_ctx back, the memory prediction that has to size the cache for the
// context actually being served, and the runner-reuse check. A load that has
// this unlocked and is then predicted at the trained context would under-count
// its cache by whatever the unlock bought.
func DCAUnlocksContext(cfg LlamaServerConfig, gpus []ml.DeviceInfo, f *gguf.Model) bool {
	if f == nil || !resolveDCAPlan(cfg).Enabled || !wouldUseOpencoti(cfg, gpus) {
		return false
	}
	return archHasDCARoute(f.KV().Architecture())
}
