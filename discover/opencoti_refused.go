package discover

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
)

// The engine refuses a device whose driver it has measured to crash or lose
// the device (since opencoti c10: RADV from Mesa 20 or older, AMD's driver
// 2.0.154 or older), and says so on one line per device:
//
//	ggml_vulkan: AMD RADV RENOIR (ACO) (driver radv 20.3.5) is REFUSED: RADV from Mesa 20 ...
//
// A listing in which every device was refused lists none and exits 1. That is
// the engine's answer, not a failed listing: falling back to llama.cpp's view
// then placed models on the refused device and every load failed with "Vulkan
// is not usable on this system" (solidPC's Renoir iGPU, 2026-10-07). So the
// refused devices are dropped, said at Warn, and the load goes where the
// engine can run it.
//
// The format is opencoti's (patch 0571), not declared as an interface yet;
// they mail before changing it (#878). A device name is the driver's free
// text and may hold parentheses, so the name ends at the LAST " (driver "
// (the greedy group). The reason is for people: never matched on. The probe
// child prints the same line, so a device can appear twice.
var refusedLineRegex = regexp.MustCompile(`^ggml_vulkan: (.+) \(driver (.*)\) is REFUSED: (.*)$`)

// engineRefusal is one device the engine will not use.
type engineRefusal struct {
	description string
	driver      string
	reason      string
}

// parseRefusals reads the engine's REFUSED lines from a listing's output.
func parseRefusals(output string) []engineRefusal {
	var out []engineRefusal
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		m := refusedLineRegex.FindStringSubmatch(strings.TrimSpace(scanner.Text()))
		if m == nil {
			continue
		}
		r := engineRefusal{description: m[1], driver: m[2], reason: m[3]}
		if !slices.ContainsFunc(out, func(o engineRefusal) bool { return o.description == r.description && o.driver == r.driver }) {
			out = append(out, r)
		}
	}
	return out
}

// refusedListing is a listing that found devices and refused every one.
type refusedListing struct {
	refusals []engineRefusal
	err      error
}

func (e *refusedListing) Error() string {
	names := make([]string, len(e.refusals))
	for i, r := range e.refusals {
		names[i] = r.description
	}
	return fmt.Sprintf("the engine refused every device (%s): %v", strings.Join(names, ", "), e.err)
}

func (e *refusedListing) Unwrap() error { return e.err }

// withRefusals turns a failed listing whose output names refused devices into
// a refusedListing, and says at Warn which devices a successful listing left
// out because it refused them.
func withRefusals(output string, err error) error {
	refused := parseRefusals(output)
	for _, r := range refused {
		slog.Warn("opencoti refuses this device's driver; it is not used",
			"device", r.description, "driver", r.driver, "reason", r.reason)
	}
	if err == nil || len(refused) == 0 {
		return err
	}
	return &refusedListing{refusals: refused, err: err}
}

// isRefusedListing reports whether err is a listing that refused every device.
func isRefusedListing(err error) bool {
	var r *refusedListing
	return errors.As(err, &r)
}
