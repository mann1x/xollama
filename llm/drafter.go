package llm

import (
	"fmt"

	"github.com/ollama/ollama/fs/gguf"
)

// The drafter decisions, as pure functions over metadata that a caller has
// already read.
//
// They exist as their own functions because two places have to reach the same
// answer and must not be able to disagree: the launch in llama_server.go, which
// puts --spec-type on the engine command line, and `xollama show`, which tells
// an operator what that command line is going to say. A `show` that recomputed
// the rule in its own words would eventually drift from the rule that runs, and
// the drift would look like a lie rather than a bug -- which is exactly how
// bug-086 read from the outside: the layer said draft-simple, show said
// draft-simple, and the engine was handed draft-mtp.
//
// Nothing here opens a file. The callers already hold the metadata -- the
// launch from LoadModel, show from the data it reads for ModelInfo -- so
// keeping these pure also keeps `show` from paying a second GGUF read.

// BuiltInDrafter reports whether a model carries its own draft head, rather
// than needing one attached beside it.
//
// Two spellings, because the field arrived after the tensors did: a
// nextn_predict_layers count is the general one, and qwen35/qwen35moe predate
// it and are recognised by their mtp.* tensors instead.
func BuiltInDrafter(arch string, nextnPredictLayers uint64, mtpTensors []gguf.TensorInfo) bool {
	if nextnPredictLayers > 0 {
		return true
	}
	return hasLegacyQwenMTPDraft(arch, mtpTensors)
}

// DraftTypeFor picks the --spec-type an attached drafter implies, from the
// drafter's own metadata. It is upstream's spelling; retargetSpecType maps it
// onto the chosen engine's.
//
// requiresTargetArch is what makes a drafter a head rather than a model: it
// carries no context of its own and has to be built against the target's.
// Launching one as draft-mtp does not degrade, it fails the load, so the
// metadata decides this and not the caller. A head built for another
// architecture is refused by name here rather than deep in the engine.
func DraftTypeFor(draftArch, requiresTargetArch, targetArch string) (string, error) {
	if draftArch == "dflash" {
		return draftTypeDFlash, nil
	}
	if requiresTargetArch != "" {
		if targetArch != "" && requiresTargetArch != targetArch {
			return "", fmt.Errorf("draft model requires a %q target, but this model is %q", requiresTargetArch, targetArch)
		}
		return draftTypeAssistant, nil
	}
	return draftTypeMTP, nil
}

// SpecTypeForShow reports the --spec-type a load of this model would use, in
// upstream's spelling, or "" when the model has no drafter at all.
//
// It is the whole launch-side rule in one call: what the drafter's metadata
// implies, then what the model's xollama.json pins over it. attached is the
// drafter's metadata when one is attached beside the model, nil when none is.
func SpecTypeForShow(attached *DrafterMetadata, targetArch string, builtIn bool, pin string) (string, error) {
	inferred := ""
	switch {
	case attached != nil:
		var err error
		inferred, err = DraftTypeFor(attached.Architecture, attached.RequiresTargetArch, targetArch)
		if err != nil {
			return "", err
		}
	case builtIn:
		inferred = draftTypeMTP
	}
	return resolveDraftType(inferred, pin), nil
}

// DrafterMetadata is the part of an attached drafter's GGUF that decides which
// driver it needs.
type DrafterMetadata struct {
	Architecture       string
	RequiresTargetArch string
}
