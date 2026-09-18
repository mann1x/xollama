// Package engine chooses which inference engine serves a model, and adapts
// ollama's llama-server launch contract to it.
//
// ollama 0.34 serves every GGML model as a llama-server subprocess, so the
// engine is a binary path and an argv behind one HTTP API. That is the whole
// seam this package works in: it never changes what ollama asks for, only
// which program is asked. With the engine resolved to llamacpp the launch is
// byte-identical to upstream, which is what keeps an A/B against vanilla
// honest.
package engine

import "runtime"

// Kind identifies an inference engine.
type Kind string

const (
	// KindLlamaCpp is the stock llama-server ollama ships.
	KindLlamaCpp Kind = "llamacpp"
	// KindOpencoti is opencoti-llamafile.
	KindOpencoti Kind = "opencoti"
)

// Platform is the OS/arch a decision is made for.
type Platform struct {
	OS   string
	Arch string
}

// Host is the platform this binary was built for.
func Host() Platform {
	return Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
}

// Backend names a compute backend exactly as ml.DeviceID.Library spells it,
// so a device list maps across without a translation table.
type Backend string

const (
	BackendCPU    Backend = "CPU"
	BackendCUDA   Backend = "CUDA"
	BackendVulkan Backend = "Vulkan"
	BackendROCm   Backend = "ROCm"
	BackendMetal  Backend = "Metal"
)

type support struct {
	Platform
	Backend Backend
}

// tested is every platform/backend combination opencoti-llamafile is validated
// on. It is data, and adding a row is a line here plus a line in the test.
//
// What is NOT here matters as much as what is. ROCm has no tested opencoti
// backend, and macOS keeps ollama's own MLX path, which this package never
// sees. Untested is not "probably fine" — it is llama.cpp.
var tested = []support{
	{Platform{OS: "linux", Arch: "amd64"}, BackendCUDA},
	{Platform{OS: "linux", Arch: "amd64"}, BackendVulkan},
	{Platform{OS: "linux", Arch: "amd64"}, BackendCPU},
	{Platform{OS: "linux", Arch: "arm64"}, BackendCUDA},
	{Platform{OS: "linux", Arch: "arm64"}, BackendCPU},
	{Platform{OS: "windows", Arch: "amd64"}, BackendCUDA},
	{Platform{OS: "windows", Arch: "amd64"}, BackendVulkan},
	{Platform{OS: "windows", Arch: "amd64"}, BackendCPU},
}

// Supports reports whether opencoti-llamafile is validated for this platform
// and backend.
func Supports(p Platform, b Backend) bool {
	for _, s := range tested {
		if s.Platform == p && s.Backend == b {
			return true
		}
	}
	return false
}
