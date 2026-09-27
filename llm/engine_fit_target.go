package llm

import "log/slog"

// opencotiEnvsForStart is extraEnvsForStart for a launch opencoti serves.
//
// Upstream pads LLAMA_ARG_FIT_TARGET by a vision projector's size plus
// mmprojOffloadHeadroom (1 GiB), "a stopgap until fit accounts for mmproj
// memory directly" (ollama/ollama#16996). opencoti's fit does account for it
// (patch 0426: the projector is placed lazily at the first image, and the
// margin is 256 MiB plus what the engine itself books). An explicit fit target
// switches that automatic margin off: the engine takes the target as its
// runtime margin and holds it out of the KV window. Measured on a V100
// (2026-09-27): 885 MiB projector + 1024 = a 1909 MiB margin, 4063 MiB of the
// KV window on the host, 4.9 GB of VRAM left unused and 2.9 tok/s.
//
// So on opencoti the pad is never added. A fit target the launch or the
// operator states is passed on unchanged: that is their choice, not ours.
func (launch llamaServerLaunchConfig) opencotiEnvsForStart() map[string]string {
	if pad, ok := launch.mmprojFitTargetMiB(); ok {
		slog.Debug("opencoti places the vision projector itself; not padding the fit target", "pad_mib", pad)
	}
	return launch.extraEnvs
}
