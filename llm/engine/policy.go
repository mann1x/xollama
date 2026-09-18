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

import (
	"fmt"
	"runtime"
)

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

// Device is one accelerator as ollama discovered it, reduced to what routing
// needs: the backend that serves it and, for CUDA, how old the silicon is.
//
// Routing on Backend alone is not enough, and the gap is not academic. A Tesla
// V100 and an RTX 4090 are both Library "CUDA" on linux/amd64, a row that is
// in the tested matrix above — yet one of them opencoti-llamafile cannot run.
type Device struct {
	Backend      Backend
	ComputeMajor int
	ComputeMinor int
}

// minCUDACompute is the oldest NVIDIA compute capability opencoti-llamafile
// carries code for, encoded as major*10+minor.
//
// Source of truth is the engine's own build. opencoti's
// vendors/sources/llamafile/llamafile/cuda.sh emits gencode for
// compute_75/80/86/89/90 and nothing older, so Maxwell (5.x), Pascal (6.x) and
// Volta (7.0) have no code in the artifact at all.
//
// llama.cpp does still cover them, but only in the cuda_v12 payload:
// llama/server/CMakePresets.json gives llama_cuda_v12_linux real SASS for
// 60/61/70, while llama_cuda_v13_* floors at 75 exactly as the engine does.
// Those cards are therefore llama.cpp's to serve, and only from the legacy
// payload — see docs/features/engine-opencoti-llamafile.md.
//
// This matters because the failure is silent. An artifact with no code for the
// device does not refuse to start; llamafile falls back to CPU and the load
// merely runs at a tenth of the speed, which reads as a performance mystery
// rather than a routing bug.
const minCUDACompute = 75

// compute encodes the capability the same way minCUDACompute is written.
// A device whose capability discovery could not read reports 0, which is below
// every floor — deliberately, see deviceUnsupported.
func (d Device) compute() int { return d.ComputeMajor*10 + d.ComputeMinor }

// String renders a device for a log line, naming the capability only when it
// is both known and meaningful for the backend.
func (d Device) String() string {
	if d.Backend == BackendCUDA && d.ComputeMajor > 0 {
		return fmt.Sprintf("%s compute %d.%d", d.Backend, d.ComputeMajor, d.ComputeMinor)
	}
	return string(d.Backend)
}

// deviceUnsupported returns the reason opencoti-llamafile cannot serve this
// device, or "" when it can. The reason is written to be readable on its own
// in a log line, because that is the only place a user will meet it.
func deviceUnsupported(p Platform, d Device) string {
	if !Supports(p, d.Backend) {
		return fmt.Sprintf("opencoti-llamafile is not tested on %s/%s with %s", p.OS, p.Arch, d.Backend)
	}
	// Unknown capability (0) is treated as unsupported rather than assumed
	// modern. Routing wrongly to llama.cpp costs the engine's features and
	// says so in the log; routing wrongly to opencoti costs a silent drop to
	// CPU. Only one of those is diagnosable from the outside.
	if d.Backend == BackendCUDA && d.compute() < minCUDACompute {
		if d.ComputeMajor == 0 {
			return "opencoti-llamafile needs a known CUDA compute capability and discovery reported none"
		}
		return fmt.Sprintf(
			"opencoti-llamafile carries no CUDA code below compute %d.%d and this device is %d.%d",
			minCUDACompute/10, minCUDACompute%10, d.ComputeMajor, d.ComputeMinor)
	}
	return ""
}

// SupportsDevice reports whether opencoti-llamafile is validated for this
// platform and this specific device.
func SupportsDevice(p Platform, d Device) bool { return deviceUnsupported(p, d) == "" }
