package xollama

import (
	"fmt"
	"strconv"
)

// The MoE expert cache (opencoti c14 r19; features moe_cache_v1,
// moe_cache_auto_v1). When a mixture-of-experts model's expert layers do not
// all fit the GPU, the engine keeps the experts those layers use most in a
// VRAM cache, so most tokens still run on the GPU. With --fit on and
// --moe-strategy auto on one GPU the engine decides by itself whether the
// cache pays (it measures an expert's upload and CPU cost at load).
//
// fit.moe_cache states the mode: auto (the engine decides; stated, it beats
// an operator's OPENCOTI_MOE_CACHE_AUTO=0), off (no automatic cache), or a
// size in MiB (--moe-cache-mib, the total over the GPUs, which turns the
// automatic sizing off). fit.moe_cache_cpu_share is --moe-cache-cpu-share,
// the share of the misses the CPU computes once the cache is full; unset is
// the engine's measured admission. fit.moe_cache_warm_start off is
// OPENCOTI_MOE_CACHE_PROFILE=0: no hot-expert profile read at load or written
// at unload.
//
// None of them raises the schema version: every older build ships an engine
// with no MoE cache, so dropping these fields leaves the model as it runs
// there anyway.
const (
	MoECacheAuto = "auto"
	MoECacheOff  = "off"
)

// maxMoECacheMiB bounds a stated size: past a large card's VRAM it is a typo.
const maxMoECacheMiB = 1 << 20

// MoECacheChoices are the named values of fit.moe_cache; a size in MiB is the
// third form.
func MoECacheChoices() []string { return []string{MoECacheAuto, MoECacheOff} }

// MoECacheMiB returns the size a stated fit.moe_cache names, or 0 for auto,
// off or nothing.
func MoECacheMiB(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 || n > maxMoECacheMiB {
		return 0
	}
	return n
}

// ValidMoECache says whether v is a fit.moe_cache value.
func ValidMoECache(v string) bool {
	return v == MoECacheAuto || v == MoECacheOff || MoECacheMiB(v) > 0
}

func (f *Fit) moeCacheIsZero() bool {
	return f == nil || (f.MoECache == "" && f.MoECacheCPUShare == nil && f.MoECacheWarmStart == nil)
}

// StatesMoECache reports whether c states any MoE cache setting.
func (c *Config) StatesMoECache() bool {
	return c != nil && !c.Fit.moeCacheIsZero()
}

// validateMoECache checks the MoE cache settings. A CPU share or a warm-start
// setting under moe_cache off has nothing to act on, and stating both is
// refused, as a rolling window under head residency is.
func (c *Config) validateMoECache(opencotiOnly func(string) error) error {
	f := c.Fit
	if f.moeCacheIsZero() {
		return nil
	}
	if f.MoECache != "" && !ValidMoECache(f.MoECache) {
		return fmt.Errorf("xollama config: fit.moe_cache %q: want auto, off or a size in MiB (1-%d)", f.MoECache, maxMoECacheMiB)
	}
	if s := f.MoECacheCPUShare; s != nil && (*s < 0 || *s > 1) {
		return fmt.Errorf("xollama config: fit.moe_cache_cpu_share %g: want a share from 0 to 1", *s)
	}
	if f.MoECache == MoECacheAuto && f.Enabled != nil && !*f.Enabled {
		return fmt.Errorf("xollama config: fit.moe_cache auto with fit.enabled off: the automatic cache is decided by the fit; turn the fit on, or state a size in MiB")
	}
	if f.MoECache == MoECacheAuto && f.MoEStrategy != "" && f.MoEStrategy != "auto" {
		return fmt.Errorf("xollama config: fit.moe_cache auto with fit.moe_strategy %s: only the auto strategy sizes the cache; set the strategy to auto, or state a size in MiB", f.MoEStrategy)
	}
	if f.MoECache == MoECacheOff {
		if f.MoECacheCPUShare != nil {
			return fmt.Errorf("xollama config: fit.moe_cache_cpu_share with fit.moe_cache off: there is no cache to share; unset one of them")
		}
		if f.MoECacheWarmStart != nil {
			return fmt.Errorf("xollama config: fit.moe_cache_warm_start with fit.moe_cache off: there is no cache to warm; unset one of them")
		}
	}
	return opencotiOnly("the MoE cache (fit.moe_cache*)")
}
