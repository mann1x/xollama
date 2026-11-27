package llm

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

// The engine names a device by its own enumeration, not xollama's. A load
// pinned to one card starts the engine with a visible-devices filter
// (GGML_VK_VISIBLE_DEVICES, CUDA_VISIBLE_DEVICES), and the engine numbers what
// it can see from 0: the RX 9070 XT that discovery calls Vulkan2 is the
// engine's Vulkan0, and every free-memory and buffer line says Vulkan0
// (eleven2go, 2026-11-21, the v0.40.17-rc.1.xollama #2362 check). Keyed by
// xollama's name, the runner found none of its own memory on the card: the
// overcommit went unreported (size_vram 100% of a load past the card) and the
// scheduler saw the card as empty with the model on it.
//
// engineDeviceLineRegex reads the engine's own name and description of each
// device it uses; the description may itself hold parentheses ("AMD
// Radeon(TM) Graphics"), the id is the last group before the free memory.
// Since c14 r19 (device_pci_v1) the line ends with the card's PCI address,
// after the free memory ("- 23332 MiB free pci=0000:11:00.0"); Metal has
// none.
var engineDeviceLineRegex = regexp.MustCompile(`using device (\S+)\s+\((.*)\)\s+\([^()]*\)\s+-\s+\d+\s+MiB free(?:\s+pci=(\S+))?`)

// noteEngineDevice records the engine's name and description of a device it
// uses. Recording alone changes nothing; engineKey reads it.
func (r *llamaServerRunner) noteEngineDevice(line []byte) {
	m := engineDeviceLineRegex.FindSubmatch(line)
	if m == nil {
		return
	}
	if r.engineDevices == nil {
		r.engineDevices = make(map[string]string)
	}
	r.engineDevices[string(m[1])] = string(m[2])
	if pci, ok := xollama.CanonicalPCIID(string(m[3])); ok {
		if r.enginePCI == nil {
			r.enginePCI = make(map[string]string)
		}
		r.enginePCI[string(m[1])] = pci
	}
}

// engineKey is the name the engine gives g in its output, the key of
// vramByDevice and systemFreeAtLoad. It is g.Name unless opencoti serves the
// load and named the device otherwise: then the engine's device of the same
// backend and description, matched in order (the engine numbers its visible
// devices in the order the filter lists them, which is s.gpus's). Stock
// llama.cpp keeps upstream's lookup (CLAUDE.md rule 2).
func (r *llamaServerRunner) engineKey(g ml.DeviceInfo) string {
	if !r.usedOpencoti || len(r.engineDevices) == 0 {
		return g.Name
	}
	// The card's PCI address, where both sides know it, is the answer: one
	// address for one card, whatever either side numbered it (c14 r19,
	// device_pci_v1). It goes first: two identical cards the two sides
	// number differently agree by name and description, and only the
	// address tells them apart.
	if want, ok := xollama.CanonicalPCIID(g.PCIID); ok {
		for name, pci := range r.enginePCI {
			if pci == want && devicePrefix(name) == devicePrefix(g.Name) {
				return name
			}
		}
	}
	// One answer per load: when every device of it is under its own name
	// with its own description, the engine saw them unfiltered. Asked per
	// device, two identical cards filtered to CUDA0 and CUDA1 would give
	// discovery's CUDA1 the engine's CUDA1, which is the other card.
	if r.namesAgree() {
		return g.Name
	}
	prefix := devicePrefix(g.Name)
	var names []string
	for name, desc := range r.engineDevices {
		if desc == g.Description && devicePrefix(name) == prefix {
			names = append(names, name)
		}
	}
	slices.SortFunc(names, func(a, b string) int { return deviceIndex(a) - deviceIndex(b) })
	k := 0
	for _, o := range r.gpus {
		if o.Name == g.Name && o.Library == g.Library {
			break
		}
		if o.Description == g.Description && devicePrefix(o.Name) == prefix {
			k++
		}
	}
	if k < len(names) {
		return names[k]
	}
	return g.Name
}

// namesAgree reports whether the engine named every device of the load as
// discovery does.
func (r *llamaServerRunner) namesAgree() bool {
	for _, o := range r.gpus {
		if desc, ok := r.engineDevices[o.Name]; !ok || desc != o.Description {
			return false
		}
	}
	return true
}

// devicePrefix is a device name without its index: "Vulkan" for "Vulkan2".
func devicePrefix(name string) string { return strings.TrimRight(name, "0123456789") }

// deviceIndex is a device name's index: 2 for "Vulkan2", -1 for none.
func deviceIndex(name string) int {
	n, err := strconv.Atoi(name[len(devicePrefix(name)):])
	if err != nil {
		return -1
	}
	return n
}
