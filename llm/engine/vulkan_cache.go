package engine

import (
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/internal/fsowner"
)

// EnvVulkanCacheDir is where opencoti keeps its Vulkan pipeline cache and the
// --vulkan-matvec-tune pick. Unset, it is under the engine's HOME, and that
// HOME is per artifact and on Windows inside lib\ollama, which the full
// installer clears: every engine and every update would start cold (162 s
// against 13 s warm on a 27B, opencoti c14 notes, bug-4278). The cache is keyed
// by device and driver, so it outlives an engine.
const EnvVulkanCacheDir = "OPENCOTI_VK_PIPELINE_CACHE_DIR"

// vulkanCacheDir is the stable folder for one OS, "" with nowhere to put it.
// On Windows it is under %LOCALAPPDATA%\xOllama, which an update leaves alone
// and an uninstall removes; ~/.ollama is shared with a stock ollama and an
// uninstall keeps it.
func vulkanCacheDir(goos, home, localAppData string) string {
	if goos == "windows" && localAppData != "" {
		return filepath.Join(localAppData, "xOllama", "engine-cache", "vulkan")
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".ollama", "engines", "vulkan-cache")
}

// defaultVulkanCacheDir is vulkanCacheDir for this process, "" when the
// operator names their own folder.
func defaultVulkanCacheDir() string {
	if envconfig.Var(EnvVulkanCacheDir) != "" {
		return ""
	}
	home, _ := os.UserHomeDir()
	return vulkanCacheDir(runtime.GOOS, home, os.Getenv("LOCALAPPDATA"))
}

// WithVulkanCache returns envs with the engine's cache folder stated. An
// operator's own setting already reaches the engine through the environment,
// and a folder that cannot be made leaves the engine's default: a cold boot is
// slower, never a failed one.
func WithVulkanCache(envs map[string]string) map[string]string {
	dir := defaultVulkanCacheDir()
	if dir == "" {
		return envs
	}
	if err := fsowner.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("cannot create the engine's Vulkan cache folder; each engine keeps its own", "dir", dir, "error", err,
			"consequence", "Vulkan loads boot cold after every engine update")
		return envs
	}
	out := make(map[string]string, len(envs)+1)
	maps.Copy(out, envs)
	out[EnvVulkanCacheDir] = dir
	return out
}
