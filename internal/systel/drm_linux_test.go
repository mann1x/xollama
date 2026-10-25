package systel

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ollama/ollama/api"
)

func writeNode(t *testing.T, dir, name string, val uint64) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strconv.FormatUint(val, 10)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A Cezanne-style iGPU: a tiny UMA carve-out plus a large shared GTT pool. Its
// reported total must be dedicated + GTT (bug-320), matching /api/xollama/devices.
func TestAMDReadingsIntegratedMemory(t *testing.T) {
	dev := t.TempDir()
	writeNode(t, dev, "mem_info_vram_total", 536870912)  // 512 MiB
	writeNode(t, dev, "mem_info_vram_used", 26038272)    // ~25 MiB
	writeNode(t, dev, "mem_info_gtt_total", 67202342912) // 62.6 GiB

	var d api.SystemDevice
	amdReadings(dev, &d)

	if !d.Integrated {
		t.Fatal("iGPU not flagged Integrated")
	}
	if want := uint64(536870912 + 67202342912); d.TotalMemory == nil || *d.TotalMemory != want {
		t.Errorf("TotalMemory = %v, want %d (dedicated + GTT)", d.TotalMemory, want)
	}
}

// A discrete board keeps its dedicated VRAM as the total; GTT must not be added.
func TestAMDReadingsDiscreteMemoryUnchanged(t *testing.T) {
	const gib = 1 << 30
	dev := t.TempDir()
	writeNode(t, dev, "mem_info_vram_total", 24*gib)
	writeNode(t, dev, "mem_info_gtt_total", 32*gib)

	var d api.SystemDevice
	amdReadings(dev, &d)

	if d.Integrated {
		t.Error("discrete GPU wrongly flagged Integrated")
	}
	if d.TotalMemory == nil || *d.TotalMemory != 24*gib {
		t.Errorf("TotalMemory = %v, want %d (dedicated only)", d.TotalMemory, uint64(24*gib))
	}
}
