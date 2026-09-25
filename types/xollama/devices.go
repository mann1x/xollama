package xollama

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Devices pins the backend, and optionally the devices, a model runs on.
//
// It belongs to the model because the choice is a property of what the model
// is for, not of the machine. A 2B model driving an agent loop wants an
// integrated GPU at idle power and must not wake the discrete card; the 30B
// model beside it wants that card and nothing else. A server-wide
// CUDA_VISIBLE_DEVICES gives every model the same answer.
//
// One backend per model. A load spans devices of one backend only — the
// engines run one ggml backend per process — so "CUDA and Vulkan" is not a
// configuration anything could serve.
type Devices struct {
	// Backend is "CUDA", "Vulkan", "ROCm" or "CPU". Required whenever IDs is
	// set: the same PCI device can be listed under two backends (a 3090 is
	// both a CUDA and a Vulkan device), so an ID alone does not say which
	// engine path to take.
	Backend string `json:"backend,omitempty"`

	// IDs selects devices within Backend. Each entry is a PCI ID
	// ("0000:18:00.0", or "18:00.0" in the default domain), the backend's
	// numeric device index, or one of the aliases "integrated" and
	// "discrete". Empty means every device of Backend. Several entries spread
	// the model across those devices, as CUDA does across cards.
	//
	// The PCI ID is the primary form because it is the only one that
	// survives a reboot, a driver update or a card being added: an index is
	// an enumeration order, and two backends number the same device
	// differently.
	IDs []string `json:"ids,omitempty"`
}

// Device aliases.
const (
	DeviceIntegrated = "integrated"
	DeviceDiscrete   = "discrete"
)

var validBackends = []string{"CUDA", "Vulkan", "ROCm", "CPU"}

// pciIDRegex matches a PCI address with or without its domain.
var pciIDRegex = regexp.MustCompile(`^(?i)([0-9a-f]{4}:)?[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)

// ValidBackends returns the backends a model may pin.
func ValidBackends() []string { return slices.Clone(validBackends) }

// CanonicalBackend returns the canonical spelling of a backend name, matched
// without regard to case, or "" when it names none.
func CanonicalBackend(s string) string {
	for _, b := range validBackends {
		if strings.EqualFold(strings.TrimSpace(s), b) {
			return b
		}
	}
	return ""
}

// CanonicalPCIID returns a PCI ID in the form discovery reports it: lower
// case, with the domain. ok is false when s is not a PCI ID.
func CanonicalPCIID(s string) (id string, ok bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !pciIDRegex.MatchString(s) {
		return "", false
	}
	if strings.Count(s, ":") == 1 {
		s = "0000:" + s
	}
	return s, true
}

// IsZero reports whether the pin says nothing.
func (d *Devices) IsZero() bool { return d == nil || (d.Backend == "" && len(d.IDs) == 0) }

// normalize rewrites the spellings Validate would otherwise refuse only on
// case, so a hand-written "vulkan" or an upper-case PCI ID is read as meant.
func (d *Devices) normalize() {
	if d == nil {
		return
	}
	if b := CanonicalBackend(d.Backend); b != "" {
		d.Backend = b
	}
	for i, id := range d.IDs {
		id = strings.TrimSpace(id)
		if pci, ok := CanonicalPCIID(id); ok {
			id = pci
		} else if a := strings.ToLower(id); a == DeviceIntegrated || a == DeviceDiscrete {
			id = a
		}
		d.IDs[i] = id
	}
}

func (d *Devices) validate() error {
	if d.IsZero() {
		return nil
	}
	if d.Backend == "" {
		return fmt.Errorf("xollama config: devices.ids needs devices.backend; a PCI device can be listed under more than one backend (want one of %v)", validBackends)
	}
	if !slices.Contains(validBackends, d.Backend) {
		return fmt.Errorf("xollama config: unknown devices.backend %q (want one of %v)", d.Backend, validBackends)
	}
	if d.Backend == "CPU" && len(d.IDs) > 0 {
		return fmt.Errorf("xollama config: devices.ids has nothing to select on the CPU backend")
	}
	seen := map[string]bool{}
	for _, id := range d.IDs {
		if id == "" {
			return fmt.Errorf("xollama config: devices.ids must not contain an empty entry")
		}
		if seen[id] {
			return fmt.Errorf("xollama config: devices.ids lists %q twice", id)
		}
		seen[id] = true
		if _, ok := CanonicalPCIID(id); ok || id == DeviceIntegrated || id == DeviceDiscrete {
			continue
		}
		if n, err := strconv.Atoi(id); err == nil && n >= 0 {
			continue
		}
		return fmt.Errorf("xollama config: devices.ids entry %q is not a PCI ID, a device index, %q or %q", id, DeviceIntegrated, DeviceDiscrete)
	}
	return nil
}
