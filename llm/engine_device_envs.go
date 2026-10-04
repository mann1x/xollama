package llm

import "github.com/ollama/ollama/ml"

// DeviceEnvs, when set, adds engine variables for a launch on gpus: the
// server's GPU policy (plans/system-settings.md) -- the split mode, and a
// forced link speed. It is set by the server package, which owns the
// policy; nil adds nothing, so a launch without it is upstream's.
var DeviceEnvs func(gpus []ml.DeviceInfo) map[string]string
