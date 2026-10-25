package systel

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
)

func writeSidecar(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gpu-metrics.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A fresh file fills the readings D3DKMT left nil, matching the device by PCI id,
// and does not touch a reading the platform already made (utilization here).
func TestSidecarEnrichesByPCIID(t *testing.T) {
	now := time.Unix(1_730_000_000, 0)
	body := `{"version":1,"updated_unix":1730000000,"gpus":[
	  {"pci_id":"0000:03:00.0","temp_c":55,"power_w":120,"power_limit_w":327,
	   "clock_mhz":2400,"clock_max_mhz":2860,"fan_pct":38,
	   "link":{"gen":4,"width":16,"max_gen":4,"max_width":16}}]}`
	r := &sidecarReader{path: writeSidecar(t, body), now: func() time.Time { return now }}

	s := &Snapshot{Devices: []api.SystemDevice{
		{Vendor: "amd", PCIID: "0000:03:00.0", Source: "d3dkmt", UtilPct: ptr(7.0)},
	}}
	r.read(s)

	d := s.Devices[0]
	if d.TempC == nil || *d.TempC != 55 {
		t.Errorf("TempC = %v, want 55", d.TempC)
	}
	if d.PowerW == nil || *d.PowerW != 120 {
		t.Errorf("PowerW = %v, want 120", d.PowerW)
	}
	if d.Link == nil || d.Link.Gen == nil || *d.Link.Gen != 4 || d.Link.Width == nil || *d.Link.Width != 16 {
		t.Errorf("Link = %+v, want gen4 x16", d.Link)
	}
	if d.UtilPct == nil || *d.UtilPct != 7 {
		t.Errorf("UtilPct = %v, want the platform's 7 (not overwritten)", d.UtilPct)
	}
}

// An entry with no bus address is the APU's integrated graphics; it matches the
// device flagged Integrated.
func TestSidecarEnrichesIntegratedWithoutPCI(t *testing.T) {
	now := time.Unix(1_730_000_000, 0)
	body := `{"version":1,"updated_unix":1730000000,"gpus":[
	  {"integrated":true,"temp_c":48,"power_w":15,"clock_mhz":2000}]}`
	r := &sidecarReader{path: writeSidecar(t, body), now: func() time.Time { return now }}

	s := &Snapshot{Devices: []api.SystemDevice{
		{Vendor: "amd", PCIID: "0000:18:00.0", Integrated: true, Source: "d3dkmt"},
	}}
	r.read(s)

	if d := s.Devices[0]; d.TempC == nil || *d.TempC != 48 {
		t.Errorf("integrated TempC = %v, want 48", d.TempC)
	}
}

// A stale file is ignored: its numbers must not reach a device.
func TestSidecarIgnoresStale(t *testing.T) {
	now := time.Unix(1_730_000_000, 0)
	body := `{"version":1,"updated_unix":1729999000,"gpus":[
	  {"pci_id":"0000:03:00.0","temp_c":55}]}` // 1000 s old
	r := &sidecarReader{path: writeSidecar(t, body), now: func() time.Time { return now }}

	s := &Snapshot{Devices: []api.SystemDevice{{PCIID: "0000:03:00.0"}}}
	r.read(s)

	if s.Devices[0].TempC != nil {
		t.Errorf("TempC = %v, want nil from a stale file", s.Devices[0].TempC)
	}
	if !r.warned {
		t.Error("a stale file should be reported once")
	}
}

// A missing file is the ordinary no-sidecar state: no change, no warning.
func TestSidecarMissingFileIsSilent(t *testing.T) {
	r := &sidecarReader{path: filepath.Join(t.TempDir(), "absent.json")}
	s := &Snapshot{Devices: []api.SystemDevice{{PCIID: "0000:03:00.0"}}}
	r.read(s)
	if s.Devices[0].TempC != nil || r.warned {
		t.Error("a missing sidecar file must be a silent no-op")
	}
}

// Malformed JSON is reported once and never panics.
func TestSidecarMalformedIsReported(t *testing.T) {
	r := &sidecarReader{path: writeSidecar(t, `{not json`)}
	s := &Snapshot{Devices: []api.SystemDevice{{PCIID: "0000:03:00.0"}}}
	r.read(s)
	if !r.warned {
		t.Error("malformed JSON should be reported once")
	}
}
