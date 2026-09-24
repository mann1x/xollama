package engine

import (
	"strings"
)

// Enumerates reports whether opencoti, rather than llama.cpp, is the engine
// that would serve devices of backend b here — and therefore the engine whose
// view of those devices discovery has to take.
//
// The two engines do not see the same devices. They ship different ggml
// builds, so they can disagree on which physical devices exist (an older
// ggml-vulkan lists a GPU once per installed ICD), on their indexes (which is
// what GGML_VK_VISIBLE_DEVICES selects by), and on how much memory each has.
// Scheduling against llama.cpp's list and then launching opencoti places a
// model on a device list the engine never reported.
//
// The per-device capability floor is not applied here, because it needs the
// capability, which is what enumeration reads. Callers apply it per device
// with SupportsDevice once they have one.
func Enumerates(p Platform, b Backend, selector string) bool {
	switch strings.ToLower(strings.TrimSpace(selector)) {
	case string(KindLlamaCpp):
		return false
	case string(KindOpencoti):
		return true
	}
	return Supports(p, b) && pinUncovered(p, b) == ""
}

// EnumerateCommand returns the command that has the engine list the devices
// of one backend, without loading a model.
//
// --gpu picks exactly one backend per process (auto chooses one, it does not
// merge), so a host with both CUDA and Vulkan devices needs one run per
// backend. --server and a model path are required even though no model is
// read: without them the artifact takes its CLI path and never reaches the
// device listing.
func EnumerateCommand(artifact string, b Backend, goos string) (string, []string) {
	args := []string{
		"--server", "--list-devices",
		"-m", "/nonexistent.gguf",
		"--offline", "--verbose",
		"--gpu", gpuFlag([]Device{{Backend: b}}),
	}
	if goos == "windows" {
		return artifact, args
	}
	return "sh", append([]string{artifact}, args...)
}
