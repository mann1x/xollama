package tweak

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/format"
	"github.com/ollama/ollama/types/xollama"
)

// liveDevices is what the server reported it can run on, from
// /api/xollama/devices. Nil against a server that predates the route, and then
// the device questions fall back to the backends the schema knows, with no
// device menu -- a pin can still be typed.
//
// It is the SERVER's view on purpose: the model runs where the server is, and
// the machine the CLI happens to run on may have different hardware or none.
//
// Fetched on first use, so a run that never reaches a device question -- every
// flags-only run that does not name one -- costs no extra request.
func liveDevices() *api.XollamaDevicesResponse {
	liveOnce.Do(func() {
		if fetchLive != nil {
			live = fetchLive()
		}
	})
	return live
}

var (
	fetchLive func() *api.XollamaDevicesResponse
	liveOnce  sync.Once
	live      *api.XollamaDevicesResponse
)

// useServerDevices points the device questions at this server, quietly: a
// device menu is a convenience, and a server that cannot list devices must not
// stop someone setting a cache type.
func useServerDevices(ctx context.Context, client *api.Client) {
	setLiveSource(func() *api.XollamaDevicesResponse {
		resp, err := client.XollamaDevices(ctx)
		if err != nil {
			return nil
		}
		return resp
	})
}

func setLiveSource(f func() *api.XollamaDevicesResponse) {
	fetchLive, liveOnce, live = f, sync.Once{}, nil
}

func devicesPin(c *xollama.Config) *xollama.Devices {
	if c.Devices == nil {
		c.Devices = &xollama.Devices{}
	}
	return c.Devices
}

func deviceBackend(c *xollama.Config) string {
	if c.Devices == nil {
		return ""
	}
	return c.Devices.Backend
}

// backendChoices offers the backends a model could actually be served on
// here: every backend with a device present, then CPU. Without a live list it
// offers every backend the schema accepts.
func backendChoices(*xollama.Config) []string {
	live := liveDevices()
	if live == nil {
		return xollama.ValidBackends()
	}
	var out []string
	for _, b := range xollama.ValidBackends() {
		if b == "CPU" || slices.ContainsFunc(live.Devices, func(d api.XollamaDevice) bool { return d.Backend == b }) {
			out = append(out, b)
		}
	}
	return out
}

// devicesOf returns the live devices of one backend, in the server's order.
func devicesOf(backend string) []api.XollamaDevice {
	live := liveDevices()
	if live == nil {
		return nil
	}
	var out []api.XollamaDevice
	for _, d := range live.Devices {
		if d.Backend == backend {
			out = append(out, d)
		}
	}
	return out
}

// selectorFor is how a menu pick is written into the config: by PCI ID where
// discovery found one, because it is the only identity that survives a reboot
// or a card being added, else by the backend's index.
func selectorFor(d api.XollamaDevice) string {
	if pci, ok := xollama.CanonicalPCIID(d.PCIID); ok {
		return pci
	}
	return d.ID
}

// deviceLine renders one device for a menu or a listing.
func deviceLine(d api.XollamaDevice) string {
	name := d.Description
	if name == "" {
		name = d.Name
	}
	var tags []string
	if d.PCIID != "" {
		tags = append(tags, d.PCIID)
	}
	tags = append(tags, "#"+d.ID)
	if d.Integrated {
		tags = append(tags, "integrated")
	}
	tags = append(tags, format.HumanBytes2(d.TotalMemory)+" total", format.HumanBytes2(d.FreeMemory)+" free")
	if d.Engine != "" {
		tags = append(tags, "served by "+d.Engine)
	}
	return fmt.Sprintf("%s %s (%s)", d.Backend, name, strings.Join(tags, ", "))
}

// describeBackends lists every device under the backend question, so the
// choice of backend is made looking at the hardware it implies.
func describeBackends(*xollama.Config) []string {
	live := liveDevices()
	if live == nil {
		return []string{"(this server does not list its devices; type a backend)"}
	}
	if len(live.Devices) == 0 {
		return []string{"no GPU found on this server -- CPU is the only backend"}
	}
	lines := []string{"devices on this server:"}
	for _, d := range live.Devices {
		lines = append(lines, "  "+deviceLine(d))
	}
	return lines
}

// deviceOptions is the device menu for the chosen backend: one entry per
// device, as the selector it would write, then "integrated"/"discrete" where
// the backend has both kinds.
func deviceOptions(c *xollama.Config) []string {
	devs := devicesOf(deviceBackend(c))
	out := make([]string, 0, len(devs)+2)
	for _, d := range devs {
		out = append(out, selectorFor(d))
	}
	hasIntegrated := slices.ContainsFunc(devs, func(d api.XollamaDevice) bool { return d.Integrated })
	hasDiscrete := slices.ContainsFunc(devs, func(d api.XollamaDevice) bool { return !d.Integrated })
	if hasIntegrated && hasDiscrete {
		out = append(out, xollama.DeviceIntegrated, xollama.DeviceDiscrete)
	}
	return out
}

func describeDevices(c *xollama.Config) []string {
	devs := devicesOf(deviceBackend(c))
	if len(devs) == 0 {
		return nil
	}
	lines := []string{deviceBackend(c) + " devices on this server:"}
	for i, d := range devs {
		lines = append(lines, fmt.Sprintf("  %d) %s", i+1, deviceLine(d)))
	}
	return lines
}

func setDeviceBackend(c *xollama.Config, v string) error {
	s := strings.TrimSpace(v)
	switch strings.ToLower(s) {
	case "", "unset", "clear", "default", "none":
		c.Devices = nil
		return nil
	}
	b := xollama.CanonicalBackend(s)
	if b == "" {
		return fmt.Errorf("want one of %s (got %q)", strings.Join(xollama.ValidBackends(), ", "), v)
	}
	d := devicesPin(c)
	// Device ids are per backend -- the same PCI address names a CUDA and a
	// Vulkan device -- so a selection made for one backend does not carry
	// over to another.
	if d.Backend != b || b == "CPU" {
		d.IDs = nil
	}
	d.Backend = b
	return nil
}

// setDeviceIDs reads a comma- or space-separated list of PCI IDs, device
// indexes and aliases. "all" is every device of the backend. A bare number is
// a device index here, never a menu number: flags pass values straight
// through, and "1" meaning the second menu line in one place and device #1 in
// another would be a trap. The wizard turns menu numbers into selectors before
// this runs.
func setDeviceIDs(c *xollama.Config, v string) error {
	s := strings.TrimSpace(v)
	switch strings.ToLower(s) {
	case "", "unset", "clear", "default", "none", "all", "any":
		if c.Devices != nil {
			c.Devices.IDs = nil
		}
		return nil
	}
	var ids []string
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if pci, ok := xollama.CanonicalPCIID(tok); ok {
			tok = pci
		} else {
			tok = strings.ToLower(tok)
		}
		if !slices.Contains(ids, tok) {
			ids = append(ids, tok)
		}
	}
	devicesPin(c).IDs = ids
	return nil
}

// resolveDeviceMenu turns the menu numbers in a wizard answer into the
// selectors those lines print, leaving anything else as typed.
func resolveDeviceMenu(raw string, options []string) string {
	toks := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' })
	if len(toks) == 0 {
		return raw
	}
	for i, tok := range toks {
		if n, err := strconv.Atoi(tok); err == nil && n >= 1 && n <= len(options) {
			toks[i] = options[n-1]
		}
	}
	return strings.Join(toks, ",")
}

func devicesBlocked(c *xollama.Config) string {
	switch deviceBackend(c) {
	case "":
		return "devices are chosen within a backend, and devices.backend is not set"
	case "CPU":
		return "the CPU backend has no devices to choose"
	}
	return ""
}

var deviceFields = []field{
	{
		name:  "device-backend",
		path:  "devices.backend",
		title: "Backend — which kind of device this model runs on",
		help: "Pin a model to CUDA, Vulkan, ROCm or the CPU. The reason to is to keep it\n" +
			"somewhere: a small model driving an agent loop on an integrated GPU, at idle\n" +
			"power, without waking the discrete card another model needs. One backend per\n" +
			"model -- an engine runs one per process. Unset lets the scheduler place it, and\n" +
			"an unpinned model is never placed on an integrated Vulkan GPU while a discrete\n" +
			"GPU is present. A pinned device that is missing refuses the load; it never\n" +
			"falls back to other hardware.",
		kind:     kindChoice,
		choices:  backendChoices,
		describe: describeBackends,
		head:     true,
		group:    []string{"device-backend", "devices"},
		get:      deviceBackend,
		set:      setDeviceBackend,
	},
	{
		name:  "devices",
		path:  "devices.ids",
		title: "Devices — which devices of that backend",
		help: "Pick one or more, by menu number or by value, separated by commas; several\n" +
			"spread the model across them. A pick is written as the device's PCI ID, the\n" +
			"one identity that survives a reboot or a card being added. A device index\n" +
			"and the words `integrated` and `discrete` are accepted too. Unset, or `all`,\n" +
			"takes every device of the backend.",
		kind:     kindDevices,
		choices:  deviceOptions,
		describe: describeDevices,
		with:     []string{"device-backend"},
		blocked:  devicesBlocked,
		get: func(c *xollama.Config) string {
			return orEmpty(c.Devices != nil, func() string { return strings.Join(c.Devices.IDs, ",") })
		},
		set: setDeviceIDs,
	},
}

// The device rows are asked straight after the engine, because where a model
// runs is decided before how its cache is shaped. They are spliced in rather
// than written into the table so the fork's table stays one literal per
// concern; every consumer still reads the one fields slice.
func init() {
	at := slices.IndexFunc(fields, func(f field) bool { return f.name == "engine" }) + 1
	fields = slices.Insert(fields, at, deviceFields...)
}
