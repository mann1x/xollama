package updater

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func put(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestThePurgeTakesOnlyOurOwnStagedUpdates(t *testing.T) {
	legacy := filepath.Join(t.TempDir(), "ollama")
	ours := put(t, legacy, "updates", "etag-a", "xOllama-darwin.zip")
	theirs := put(t, legacy, "updates", "etag-b", "Ollama-darwin.zip")
	theirMarker := put(t, legacy, "upgraded")
	theirBackup := put(t, legacy, "backup", "Ollama.app", "Contents", "Info.plist")

	if purgeLegacyStage(legacy, "updates", []string{"xOllama-darwin.zip"}, "xOllama.app") {
		t.Fatal("reported an upgrade with no backup of ours present")
	}
	if exists(ours) || exists(filepath.Dir(ours)) {
		t.Fatalf("our staged update is still there: %s", ours)
	}
	for _, p := range []string{theirs, theirMarker, theirBackup} {
		if !exists(p) {
			t.Fatalf("a stock Ollama's file was removed: %s", p)
		}
	}
}

func TestThePurgeFindsTheUpgradeAnOlderBuildPerformed(t *testing.T) {
	legacy := filepath.Join(t.TempDir(), "ollama")
	backup := put(t, legacy, "backup", "xOllama.app", "Contents", "Info.plist")
	marker := put(t, legacy, "upgraded")

	if !purgeLegacyStage(legacy, "updates", []string{"xOllama-darwin.zip"}, "xOllama.app") {
		t.Fatal("our backup was there and no upgrade was reported")
	}
	if exists(backup) || exists(marker) || exists(legacy) {
		t.Fatalf("left behind: backup %v marker %v folder %v", exists(backup), exists(marker), exists(legacy))
	}
}

// With the other app's backup beside ours, the marker cannot be told apart
// and stays; a stock Ollama that finds it only runs its own cleanup.
func TestTheMarkerStaysWhenTheBackupFolderIsNotOnlyOurs(t *testing.T) {
	legacy := filepath.Join(t.TempDir(), "ollama")
	put(t, legacy, "backup", "xOllama.app", "Contents", "Info.plist")
	theirs := put(t, legacy, "backup", "Ollama.app", "Contents", "Info.plist")
	marker := put(t, legacy, "upgraded")

	if !purgeLegacyStage(legacy, "updates", []string{"xOllama-darwin.zip"}, "xOllama.app") {
		t.Fatal("no upgrade reported")
	}
	if !exists(theirs) || !exists(marker) {
		t.Fatalf("their backup %v, marker %v: both must stay", exists(theirs), exists(marker))
	}
}

func TestThePurgeOfNothingIsQuiet(t *testing.T) {
	if purgeLegacyStage(filepath.Join(t.TempDir(), "ollama"), "updates", []string{"xOllama-darwin.zip"}, "xOllama.app") {
		t.Fatal("reported an upgrade in an empty place")
	}
}

func TestMarkUpgradedCreatesItsFolder(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "xOllama", "upgraded")
	markUpgraded(marker)
	if !exists(marker) {
		t.Fatal("no marker")
	}
}

// The macOS file is not compiled on the hosts most tests run on, so its source
// is read: it must take its folder from forkCacheDir and never name upstream's.
func TestTheMacUpdaterStagesInItsOwnFolder(t *testing.T) {
	src, err := os.ReadFile("updater_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, "appDataDir := forkCacheDir(cacheDir)") {
		t.Fatal("updater_darwin.go does not take its cache folder from forkCacheDir")
	}
	if strings.Contains(s, `filepath.Join(cacheDir, "ollama")`) {
		t.Fatal("updater_darwin.go names upstream's cache folder")
	}
	if forkCacheDir("/c") == filepath.Join("/c", upstreamCacheName) {
		t.Fatal("the fork's cache folder is upstream's")
	}
}
