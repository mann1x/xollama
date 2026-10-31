package llm

import (
	"errors"
	"strconv"

	"github.com/ollama/ollama/llm/engine"
	"github.com/ollama/ollama/types/xollama"
)

// appendEnginePolicyArgs adds the engine's automatic policies a model, or
// the server's defaults under it, state (types/xollama/engine_policy.go).
// Nothing stated adds nothing: the engine's own defaults apply, as they did.
//
// --fit is upstream llama.cpp's too, so it reaches either engine. The rest
// are opencoti's; types/xollama refuses them on a model pinned to stock
// llama.cpp, so reaching here with one set and llama.cpp chosen means the
// engine was switched underneath the model, and the flag is left off rather
// than passed to an engine that would reject the whole command line.
func appendEnginePolicyArgs(args []string, x *xollama.Config, usedOpencoti bool) []string {
	if x == nil {
		return args
	}
	if x.Fit != nil && x.Fit.Enabled != nil {
		v := "off"
		if *x.Fit.Enabled {
			v = "on"
		}
		args = append(args, "--fit", v)
	}
	if !usedOpencoti {
		return args
	}
	if x.Fit != nil && x.Fit.VRAMTargetMiB > 0 {
		args = append(args, "--vram-target", strconv.Itoa(x.Fit.VRAMTargetMiB))
	}
	if x.KV != nil && x.KV.RollingWindow != "" {
		args = append(args, "--kv-rolling-window", x.KV.RollingWindow)
	}
	if x.Draft != nil && x.Draft.AutoMTPPolicy != "" {
		args = append(args, "--auto-mtp-policy", x.Draft.AutoMTPPolicy)
	}
	if x.KV != nil && x.KV.AdmissionPoolless != "" {
		args = append(args, "--admission-poolless", x.KV.AdmissionPoolless)
	}
	if x.Fit != nil && x.Fit.MoEStrategy != "" {
		args = append(args, "--moe-strategy", x.Fit.MoEStrategy)
	}
	if x.KV != nil && x.KV.VulkanMatvecTune != nil {
		v := "off"
		if *x.KV.VulkanMatvecTune {
			v = "on"
		}
		args = append(args, "--vulkan-matvec-tune", v)
	}
	return appendEngineTuningArgs(args, x.Tuning)
}

// policyUnavailable is engine.PolicyUnavailable; a variable so a test can state
// either answer.
var policyUnavailable = engine.PolicyUnavailable

// enginePolicyRefusal refuses a policy the pinned engine does not declare. Sent
// anyway, the engine would reject the whole command line with "invalid
// argument", which names nothing the operator can act on.
func enginePolicyRefusal(x *xollama.Config, usedOpencoti bool) error {
	if x == nil || !usedOpencoti {
		return nil
	}
	check := func(stated bool, setting, feature string) error {
		if !stated {
			return nil
		}
		if why := policyUnavailable(setting, feature); why != "" {
			return errors.New(why)
		}
		return nil
	}
	return errors.Join(
		check(x.KV != nil && x.KV.VulkanMatvecTune != nil, "kv.vulkan_matvec_tune", engine.FeatureVulkanMatvecTune),
		check(x.Fit != nil && x.Fit.MoEStrategy != "", "fit.moe_strategy", engine.FeatureMoEStrategy),
		check(x.Draft != nil && x.Draft.AutoMTPPolicy == xollama.AutoMTPAdaptive, "draft.auto_mtp_policy adaptive", engine.FeatureAutoMTPAdaptive),
	)
}
