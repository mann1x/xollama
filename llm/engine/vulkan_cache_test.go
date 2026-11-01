package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTheVulkanCacheOutlivesTheEnginesPayload(t *testing.T) {
	win := vulkanCacheDir("windows", `C:\Users\a`, `C:\Users\a\AppData\Local`)
	if !strings.Contains(win, "xOllama") {
		t.Fatalf("windows folder %q is not the one an uninstall removes", win)
	}
	// The full installer clears lib\ollama, and the payload HOME is under it.
	if strings.Contains(win, filepath.Join("lib", "ollama")) {
		t.Fatalf("windows folder %q is under what the installer clears", win)
	}
	if got := vulkanCacheDir("linux", "/home/a", ""); got != filepath.Join("/home/a", ".ollama", "engines", "vulkan-cache") {
		t.Fatalf("linux folder = %q", got)
	}
	if strings.Contains(vulkanCacheDir("linux", "/home/a", ""), filepath.Base(payloadMarker)) {
		t.Fatal("the cache must not sit in a payload directory")
	}
	if got := vulkanCacheDir("linux", "", ""); got != "" {
		t.Fatalf("no home gave %q", got)
	}
}

func TestAnEngineIsToldTheStableVulkanCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LOCALAPPDATA", home)
	t.Setenv("XOLLAMA_"+EnvVulkanCacheDir, "")
	t.Setenv(EnvVulkanCacheDir, "")

	in := map[string]string{"HOME": "/payload"}
	got := WithVulkanCache(in)
	if _, changed := in[EnvVulkanCacheDir]; changed {
		t.Fatal("the caller's map was written")
	}
	want := vulkanCacheDir(runtime.GOOS, home, home)
	if got[EnvVulkanCacheDir] != want || got["HOME"] != "/payload" {
		t.Fatalf("envs = %v, want cache %q and HOME kept", got, want)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Fatalf("the folder was not made: %v", err)
	}
}

func TestAnOperatorsVulkanCacheFolderIsLeftAlone(t *testing.T) {
	t.Setenv(EnvVulkanCacheDir, "/mine")
	in := map[string]string{"HOME": "/payload"}
	got := WithVulkanCache(in)
	if _, set := got[EnvVulkanCacheDir]; set {
		t.Fatalf("envs = %v: the operator's value reaches the engine by itself", got)
	}
	if defaultVulkanCacheDir() != "" {
		t.Fatal("an operator's folder must never be adopted as ours")
	}
}

func TestAFolderThatCannotBeMadeLeavesTheEnginesDefault(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".ollama"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LOCALAPPDATA", filepath.Join(home, ".ollama"))
	t.Setenv(EnvVulkanCacheDir, "")
	if got := WithVulkanCache(map[string]string{"HOME": "/payload"}); got[EnvVulkanCacheDir] != "" {
		t.Fatalf("envs = %v", got)
	}
}
