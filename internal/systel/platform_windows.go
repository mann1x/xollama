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
		// D3DKMT does not expose, from a metrics sidecar if one is installed.
		&sidecarReader{path: windowsSidecarPath()},
	}
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
