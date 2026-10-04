package xollama

import (
	"strconv"
	"strings"
)

// A device named by what it is called.
//
// A PCI ID is the identity to pin by, where discovery has one. Vulkan on
// Windows gives none, and there the backend's index is all that is left: an
// enumeration order, which on one machine put the same card at Vulkan1,
// Vulkan2 and Vulkan1 again across three boots of one day. The name does not
// move. "name:AMD Radeon RX 9070 XT" is that device wherever the backend
// lists it; "#2" after the name is the second of several devices with that
// name, in the backend's order, and without it the selector names all of them.
//
// Two cards of one name are only as far apart as their order: with no PCI ID
// nothing else distinguishes them, and that order can change.
const deviceNamePrefix = "name:"

// NameSelector is the selector for a device called name: the ordinal-th of
// those with that name when ordinal is positive, every one of them otherwise.
func NameSelector(name string, ordinal int) string {
	name = cleanDeviceName(name)
	if name == "" {
		return ""
	}
	if ordinal > 0 {
		return deviceNamePrefix + name + "#" + strconv.Itoa(ordinal)
	}
	return deviceNamePrefix + name
}

// ParseNameSelector reads a name selector. ordinal is 0 when it names every
// device of that name.
func ParseNameSelector(s string) (name string, ordinal int, ok bool) {
	s = strings.TrimSpace(s)
	if len(s) < len(deviceNamePrefix) || !strings.EqualFold(s[:len(deviceNamePrefix)], deviceNamePrefix) {
		return "", 0, false
	}
	rest := s[len(deviceNamePrefix):]
	if i := strings.LastIndexByte(rest, '#'); i >= 0 {
		if n, err := strconv.Atoi(rest[i+1:]); err == nil && n >= 1 && !strings.ContainsAny(rest[i+1:], "+- ") {
			ordinal, rest = n, rest[:i]
		}
	}
	name = cleanDeviceName(rest)
	return name, ordinal, name != ""
}

// SameDeviceName compares two device names as a selector does: without
// regard to case or to runs of spaces.
func SameDeviceName(a, b string) bool {
	a, b = cleanDeviceName(a), cleanDeviceName(b)
	return a != "" && strings.EqualFold(a, b)
}

// NameSelects reports whether the selector (name, ordinal) picks the device
// called deviceName that is the deviceOrdinal-th of its name. A
// deviceOrdinal of 0 is a device whose place among its namesakes is not
// known; it is taken for the first.
func NameSelects(name string, ordinal int, deviceName string, deviceOrdinal int) bool {
	if !SameDeviceName(name, deviceName) {
		return false
	}
	return ordinal == 0 || ordinal == max(deviceOrdinal, 1)
}

// CanonicalDeviceKey returns a stable device identity in its canonical
// spelling: a PCI ID or a name selector. ok is false for anything else, a
// device index included.
func CanonicalDeviceKey(s string) (key string, ok bool) {
	if pci, ok := CanonicalPCIID(s); ok {
		return pci, true
	}
	if name, ordinal, ok := ParseNameSelector(s); ok {
		return NameSelector(name, ordinal), true
	}
	return "", false
}

// sameDeviceKey compares two canonical keys; names differ only by case.
func sameDeviceKey(a, b string) bool { return strings.EqualFold(a, b) }

func cleanDeviceName(s string) string { return strings.Join(strings.Fields(s), " ") }
