package xollama

import (
	"fmt"
	"slices"
	"strconv"
)

// The engine's automatic policies a model, or the server's defaults, can
// state (plans/system-settings.md; the full list, opencoti #609). Each has
// an engine default that applies when nothing is stated.

// Fit is the engine's automatic fit of layers and KV into memory.
type Fit struct {
	// Enabled is --fit on|off. The engine's default is on.
	Enabled *bool `json:"enabled,omitempty"`
	// VRAMTargetMiB is opencoti's --vram-target: the memory the fit and
	// the rolling-KV sizer may use. Zero is the engine's default, all free
	// VRAM minus the compute reserve.
	VRAMTargetMiB int `json:"vram_target_mib,omitempty"`
	// MoEStrategy is opencoti's --moe-strategy: which part of a MoE model the
	// fit gives the device first when it does not all fit. Unset is the
	// engine's default, auto.
	MoEStrategy string `json:"moe_strategy,omitempty"`
}

// IsZero reports whether f states nothing.
func (f *Fit) IsZero() bool {
	return f == nil || (f.Enabled == nil && f.VRAMTargetMiB == 0 && f.MoEStrategy == "")
}

// MoE strategies, the engine's --moe-strategy values.
var validMoEStrategies = []string{"auto", "experts-gpu", "kv-gpu"}

// ValidMoEStrategies returns the --moe-strategy values.
func ValidMoEStrategies() []string { return slices.Clone(validMoEStrategies) }

// Admission gate modes, the engine's --admission-poolless values.
var validAdmissionPoolless = []string{"off", "warn", "enforced"}

// ValidAdmissionPoolless returns the --admission-poolless values.
func ValidAdmissionPoolless() []string { return slices.Clone(validAdmissionPoolless) }

// AutoMTPAdaptive is the --auto-mtp-policy value that needs a c14 engine.
const AutoMTPAdaptive = "adaptive"

// Rolling window values; anything else must be a size in MiB.
const (
	RollingOn  = "on"
	RollingOff = "off"
)

// maxRollingMiB bounds a stated window: opencoti's own auto size is 64 to
// 512 MiB, and a figure past a large card's VRAM is a typo.
const maxRollingMiB = 1 << 16

// MTP policies, the engine's --auto-mtp-policy values. measured is its
// default: draft when the measured acceptance pays.
var validAutoMTPPolicies = []string{"measured", "allocator", "taper", "always", AutoMTPAdaptive, "off"}

// ValidAutoMTPPolicies returns the --auto-mtp-policy values.
func ValidAutoMTPPolicies() []string { return slices.Clone(validAutoMTPPolicies) }

// ValidRollingWindow says whether v is a --kv-rolling-window value.
func ValidRollingWindow(v string) bool {
	if v == RollingOn || v == RollingOff {
		return true
	}
	n, err := strconv.Atoi(v)
	return err == nil && n > 0 && n <= maxRollingMiB
}

// RollingWindowOn reports whether a stated window asks for the rolling
// window: on, or a size.
func RollingWindowOn(v string) bool { return v != "" && v != RollingOff }

// setsC14Policies reports the policies an engine before c14 does not have.
func (c *Config) setsC14Policies() bool {
	return (c.KV != nil && c.KV.VulkanMatvecTune != nil) ||
		(c.Fit != nil && c.Fit.MoEStrategy != "") ||
		(c.Draft != nil && c.Draft.AutoMTPPolicy == AutoMTPAdaptive)
}

func (c *Config) setsEnginePolicies() bool {
	return !c.Fit.IsZero() ||
		(c.KV != nil && (c.KV.RollingWindow != "" || c.KV.AdmissionPoolless != "")) ||
		(c.Draft != nil && c.Draft.AutoMTPPolicy != "")
}

// validateEnginePolicies checks the policies against each other and the
// engine. Only combinations opencoti defines are accepted (#609): head
// residency disables the position window, so a rolling window under it is a
// setting with no effect, and stating both is refused.
func (c *Config) validateEnginePolicies() error {
	if err := c.validateTuning(); err != nil {
		return err
	}
	opencotiOnly := func(what string) error {
		if c.Engine == EngineLlamaCpp {
			return fmt.Errorf("xollama config: %s needs the opencoti engine; this config pins engine %q", what, c.Engine)
		}
		return nil
	}
	if c.KV != nil && c.KV.RollingWindow != "" {
		if !ValidRollingWindow(c.KV.RollingWindow) {
			return fmt.Errorf("xollama config: kv.rolling_window %q: want on, off or a size in MiB (1-%d)", c.KV.RollingWindow, maxRollingMiB)
		}
		if err := opencotiOnly("kv.rolling_window"); err != nil {
			return err
		}
		if RollingWindowOn(c.KV.RollingWindow) && c.KV.ResidencyMode == ResidencyHead {
			return fmt.Errorf("xollama config: kv.rolling_window %q with kv.residency_mode head: head keeps every cell on the device and disables the window; use auto or window", c.KV.RollingWindow)
		}
	}
	if c.Draft != nil && c.Draft.AutoMTPPolicy != "" {
		if !slices.Contains(validAutoMTPPolicies, c.Draft.AutoMTPPolicy) {
			return fmt.Errorf("xollama config: unknown draft.auto_mtp_policy %q (want one of %v)", c.Draft.AutoMTPPolicy, validAutoMTPPolicies)
		}
		if err := opencotiOnly("draft.auto_mtp_policy"); err != nil {
			return err
		}
	}
	if c.KV != nil && c.KV.AdmissionPoolless != "" {
		if !slices.Contains(validAdmissionPoolless, c.KV.AdmissionPoolless) {
			return fmt.Errorf("xollama config: unknown kv.admission_poolless %q (want one of %v)", c.KV.AdmissionPoolless, validAdmissionPoolless)
		}
		if err := opencotiOnly("kv.admission_poolless"); err != nil {
			return err
		}
	}
	if c.KV != nil && c.KV.VulkanMatvecTune != nil {
		if err := opencotiOnly("kv.vulkan_matvec_tune"); err != nil {
			return err
		}
	}
	if c.Fit != nil && c.Fit.MoEStrategy != "" {
		if !slices.Contains(validMoEStrategies, c.Fit.MoEStrategy) {
			return fmt.Errorf("xollama config: unknown fit.moe_strategy %q (want one of %v)", c.Fit.MoEStrategy, validMoEStrategies)
		}
		if err := opencotiOnly("fit.moe_strategy"); err != nil {
			return err
		}
	}
	if c.Fit != nil {
		if c.Fit.VRAMTargetMiB < 0 {
			return fmt.Errorf("xollama config: fit.vram_target_mib %d is negative", c.Fit.VRAMTargetMiB)
		}
		if c.Fit.VRAMTargetMiB > 0 {
			if err := opencotiOnly("fit.vram_target_mib"); err != nil {
				return err
			}
		}
	}
	return nil
}
