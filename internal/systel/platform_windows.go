package systel

import (
	"os"
	"path/filepath"
)

func platformReaders(root int) []reader {
	return []reader{
		&procReader{root: root, p: windowsPlatform{}},
		nvmlReader{},
		&d3dkmtReader{},
		// Last: fill the temperature, power, clocks, fan and PCIe link that
		// D3DKMT does not expose, from the metrics sidecar, run while polled.
		newWindowsSidecarReader(windowsSidecarPath()),
	}
}

func newWindowsSidecarReader(path string) *sidecarReader {
	r := &sidecarReader{path: path}
	// A typed nil in the interface would not read as "no process".
	if run := newSidecarRunner(path); run != nil {
		r.proc = run
	}
	return r
}

// windowsSidecarPath is where the metrics sidecar writes, beside the app's own
// state under %LOCALAPPDATA%\xOllama (the app-state hook). Empty when that
// location cannot be resolved, which disables the reader.
func windowsSidecarPath() string {
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "xOllama", "gpu-metrics.json")
}
