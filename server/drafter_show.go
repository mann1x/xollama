package server

import (
	"path/filepath"
	"strings"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/format"
	"github.com/ollama/ollama/fs/gguf"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/types/xollama"
)

// drafterShowInfo describes the drafter a load of this model would use, or nil
// when it has none and none is published for it.
//
// A drafter is invisible in every other part of a show response. An attached
// one is a manifest layer, a built-in one is a handful of tensors, and neither
// appears in ModelInfo, Parameters or Details -- so before this, a model could
// carry a 440 MiB drafter and give no sign of it, and a `draft.spec_type` in
// the xollama section had nothing to say what it was overriding.
//
// It reports the RESOLVED driver, pin included, because that is the question
// an operator actually has. Both halves of the rule come from llm, so this
// cannot drift from what the launch does.
//
// It also names the drafter published for the model (xollama.RecommendedDrafter),
// attached or not: a model that could draft and does not is the case an
// operator most needs to see, and `xollama tweak model --drafter` reads its
// answer from here.
//
// Failing to describe a drafter is never fatal: `show` is how someone finds out
// their model is misconfigured, and refusing to print anything is the least
// helpful moment to be strict. A drafter that cannot be resolved -- a head
// built for another architecture, say -- is reported with an empty SpecType,
// which is honest about there being no answer rather than inventing one.
func drafterShowInfo(m *Model, targetKV *gguf.Metadata, targetTensors gguf.Tensors, pin string) *api.DrafterInfo {
	var attached *llm.DrafterMetadata
	info := &api.DrafterInfo{}

	targetArch, builtIn := "", false
	var targetWidth uint64
	if targetKV != nil {
		targetArch = targetKV.Architecture()
		targetWidth = targetKV.Uint("embedding_length")
		builtIn = llm.BuiltInDrafter(targetArch, targetKV.Uint("nextn_predict_layers"), targetTensors.Items("mtp."))
		if ref, ok := xollama.RecommendedDrafter(targetArch, targetWidth); ok {
			info.Recommended = &api.DrafterRecommendation{Target: ref.Target, Source: ref.Source(), SpecType: ref.SpecType, Tokens: ref.Tokens}
		}
	}

	if m.DraftPath != "" {
		draftKV, _, err := getModelData([]string{m.DraftPath}, false)
		if err != nil {
			return nil
		}
		attached = &llm.DrafterMetadata{
			Architecture:       draftKV.Architecture(),
			RequiresTargetArch: draftKV.String("requires_target_arch"),
		}
		info.Source = "attached"
		info.Architecture = draftKV.Architecture()
		info.QuantizationLevel = draftKV.FileType().String()
		if n := draftKV.Uint("general.parameter_count"); n > 0 {
			info.ParameterSize = format.HumanNumber(n)
		}
		info.Digest = blobDigest(m.DraftPath)
		if err := xollama.DrafterFits(draftKV.Uint("embedding_length_out"), targetWidth); err != nil {
			info.Mismatch = err.Error()
		} else if info.Recommended != nil && draftKV.Uint("embedding_length_out") == targetWidth {
			info.Recommended.Attached = true
		}
	}

	if attached == nil {
		switch {
		case builtIn:
			info.Source = "built-in"
		case info.Recommended != nil:
			info.Source = api.DrafterSourceNone
			return info
		default:
			return nil
		}
	}

	info.Tokens, info.TokensFrom = drafterTokens(m, attached != nil)

	// A pin only counts as one when there is a drafter for it to apply to;
	// SpecTypeForShow returns "" for a model with none, and so must this.
	specType, err := llm.SpecTypeForShow(attached, targetArch, builtIn, pin)
	if err != nil {
		return info
	}
	info.SpecType = specType
	info.Pinned = pin != "" && specType == pin

	return info
}

// drafterTokens is the draft length a load of this model passes when the
// request states none, and who chose it. It reads the same options the load
// does, so the two cannot disagree.
func drafterTokens(m *Model, attached bool) (int, string) {
	opts := api.DefaultOptions()
	_ = opts.FromMap(m.GenerationDefaults)
	_ = opts.FromMap(m.Options)
	set := applyDraftTokens(m, &opts, hasOption(m.Options, "draft_num_predict"))
	switch {
	case set:
		return opts.DraftNumPredict, "model"
	case attached:
		return opts.DraftNumPredict, "default"
	default:
		return 0, "engine"
	}
}

// blobDigest reads a blob's digest back from its file name.
func blobDigest(path string) string {
	name := filepath.Base(path)
	if hex, ok := strings.CutPrefix(name, "sha256-"); ok {
		return "sha256:" + hex
	}
	return ""
}

// drafterSpecTypePin is the model's pinned driver, or "" when it states none.
func drafterSpecTypePin(m *Model) string {
	if m.Xollama == nil || m.Xollama.Draft == nil {
		return ""
	}
	return m.Xollama.Draft.SpecType
}
