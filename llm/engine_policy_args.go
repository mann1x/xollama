package llm

import (
	"strconv"

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
	return args
}
