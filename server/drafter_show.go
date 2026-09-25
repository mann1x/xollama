package server

import (
	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/format"
	"github.com/ollama/ollama/fs/gguf"
	"github.com/ollama/ollama/llm"
)

// drafterShowInfo describes the drafter a load of this model would use, or nil
// when it has none.
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
// Failing to describe a drafter is never fatal: `show` is how someone finds out
// their model is misconfigured, and refusing to print anything is the least
// helpful moment to be strict. A drafter that cannot be resolved -- a head
// built for another architecture, say -- is reported with an empty SpecType,
// which is honest about there being no answer rather than inventing one.
func drafterShowInfo(m *Model, targetKV *gguf.Metadata, targetTensors gguf.Tensors, pin string) *api.DrafterInfo {
	var attached *llm.DrafterMetadata
	info := &api.DrafterInfo{}

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
	}

	targetArch, builtIn := "", false
	if targetKV != nil {
		targetArch = targetKV.Architecture()
		builtIn = llm.BuiltInDrafter(targetArch, targetKV.Uint("nextn_predict_layers"), targetTensors.Items("mtp."))
	}
	if attached == nil {
		if !builtIn {
			return nil
		}
		info.Source = "built-in"
	}

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

// drafterSpecTypePin is the model's pinned driver, or "" when it states none.
func drafterSpecTypePin(m *Model) string {
	if m.Xollama == nil || m.Xollama.Draft == nil {
		return ""
	}
	return m.Xollama.Draft.SpecType
}
