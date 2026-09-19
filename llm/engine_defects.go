package llm

import (
	"fmt"
	"path/filepath"
	"strings"
)

// xollama-hook: engine-defects
//
// Naming a defect in an engine build we ship and cannot patch.
//
// The engine artifact is pinned by sha and bundled into the release payload, so
// when a published cut carries a bug there is nothing to upgrade to until the
// next one is published. What is still in our gift is the failure message. An
// abort from inside somebody else's CUDA kernel is unreadable:
//
//	fattn-common.cuh:87: GGML_ASSERT(dst->op == GGML_OP_FLASH_ATTN_EXT) failed
//
// Nobody meeting that can tell whether their model is broken, their card is
// broken, their settings are wrong, or the engine has a known bug with a known
// workaround. This turns the ones we know about into the last of those.
//
// This is diagnosis, never recovery. It does not retry, downgrade, or change
// what was launched -- a load that fails still fails, with the same error,
// wrapped in the explanation. Falling back silently is what XOLLAMA_ENGINE_FALLBACK
// is for, and it is off by default for the reason given on retryOnStockEngine.

// knownEngineDefect is one defect in a published artifact.
//
// Both halves must match before anything is said. Version alone would blame the
// build for every unrelated failure a user has while running it; signature
// alone would blame a defect that a newer artifact has already fixed. Together
// they are specific enough that the message can be stated as fact.
type knownEngineDefect struct {
	// Versions are substrings matched against the artifact's file name. The
	// name carries the cut, and the pin binds the name to a sha, so this is
	// exact without hashing 600 MB on a failure path.
	Versions []string
	// Signatures are substrings matched against the engine's own dying words.
	Signatures []string
	// Summary says what is wrong, and Workaround what to do instead. Both are
	// written to be read by someone who has just had a load fail and does not
	// know any of this.
	Summary    string
	Workaround string
}

// knownEngineDefects is the table. A row is retired the day a published
// artifact without the defect is pinned -- it describes a specific build, not a
// permanent property of the engine.
var knownEngineDefects = []knownEngineDefect{
	{
		// opencoti bug-3369: a 0.10.5-port regression in the streaming-attention
		// fallbacks. Fixed by their patch 0253, two days after c7 was published
		// and never announced; c7 is still the only published cut, so there is
		// nothing to re-pin to. Reported to us 2026-09-19; the same abort was
		// independently measured in docs/evaluations/phase2-engine-ab.md before
		// it had a name.
		Versions: []string{"0.10.5-c7"},
		Signatures: []string{
			"GGML_ASSERT(dst->op == GGML_OP_FLASH_ATTN_EXT)",
			"fattn-common.cuh",
			"ggml_new_object: not enough space in the context's memory pool",
		},
		Summary: "this build of the opencoti engine (0.10.5-c7) aborts when the KV cache does not fit in VRAM and starts spilling to host memory, on either cache layout",
		Workaround: "keep the cache resident rather than relying on spill: lower num_ctx, " +
			"or compress the cache with OLLAMA_KV_CACHE_TYPE=q8_0 (or XOLLAMA_K_CACHE_TYPE / " +
			"XOLLAMA_V_CACHE_TYPE per half). Serving this model on stock llama.cpp instead, " +
			"with XOLLAMA_ENGINE=llamacpp, also avoids it",
	},
}

// describeEngineDefect returns an explanation when a failure matches a known
// defect in the artifact that produced it, or "" when it does not.
//
// artifact is the engine binary's path; output is whatever the engine said on
// its way down. Matching is case-insensitive on the signature because the same
// assertion reaches us through several log paths.
func describeEngineDefect(artifact, output string) string {
	name := strings.ToLower(filepath.Base(artifact))
	lower := strings.ToLower(output)

	for _, d := range knownEngineDefects {
		if !containsAny(name, d.Versions) || !containsAny(lower, d.Signatures) {
			continue
		}
		return fmt.Sprintf("this is a known defect in the pinned engine build, not a problem with your model: %s. Until a fixed build is published, %s", d.Summary, d.Workaround)
	}
	return ""
}

// containsAny reports whether s contains any of the needles, which are compared
// in lower case because the callers lower s.
func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

// annotateEngineDefect wraps a load failure with an explanation when it matches
// a known defect. The original error is preserved underneath, wrapped rather
// than replaced, so nothing downstream that inspects it stops working.
func (s *llamaServerRunner) annotateEngineDefect(err error) error {
	if err == nil || !s.usedOpencoti || s.cmd == nil {
		return err
	}
	// The engine's own output reaches us inside the error the load returned;
	// the status writer holds it too when the error was built before the line
	// arrived.
	why := describeEngineDefect(s.cmd.Path, err.Error()+"\n"+s.status.LastError())
	if why == "" {
		return err
	}
	return fmt.Errorf("%w — %s", err, why)
}
