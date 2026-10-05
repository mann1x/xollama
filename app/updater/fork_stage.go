package updater

import (
	"log/slog"
	"os"
	"path/filepath"
)

// xOllama stages its updates in a folder of its own.
//
// Upstream keeps three things under the user's cache folder, in a directory
// named after itself: the downloaded update, the backup of the app being
// replaced, and the marker a finished upgrade leaves for the next start. A fork
// that keeps the name shares all three with a stock Ollama on the same account,
// and none of them is shareable: cleanupOldDownloads removes EVERYTHING in the
// stage directory, so each app deleted the other's download; a staged archive
// that is not ours fails verification and is deleted for it; the backup
// directory and the marker are one name for two apps, so one upgrade could make
// the other's refuse ("prior upgrade failed") or skip its cleanup.
//
// macOS was the last platform still doing it (Windows moved with the app-state
// hook). It only started to matter with the first release that publishes
// xOllama-darwin.zip, since before it the updater had nothing to download.

// forkCacheName is the directory under the user's cache folder that holds the
// stage directory, the backup and the marker.
const forkCacheName = "xOllama"

// upstreamCacheName is the directory builds before this one used on macOS.
const upstreamCacheName = "ollama"

func forkCacheDir(userCache string) string {
	return filepath.Join(userCache, forkCacheName)
}

// purgeLegacyStage removes what an older xOllama left in the directory it
// shared with a stock Ollama, and only that: staged updates whose file name is
// one of ours, and the backup of our own bundle. Anything else there belongs to
// the other app and is not touched, and a directory is removed only once it is
// empty.
//
// It reports whether our backup was found. That is the state an older xOllama
// leaves when it has just upgraded to this build: it wrote its marker where
// this build no longer looks, so the caller has to record the upgrade itself.
// In that case the marker is ours too and goes with the backup.
func purgeLegacyStage(legacyDir, stageName string, ours []string, backupRoot string) (upgraded bool) {
	stage := filepath.Join(legacyDir, stageName)
	for _, name := range ours {
		files, _ := filepath.Glob(filepath.Join(stage, "*", name))
		for _, f := range files {
			if err := os.Remove(f); err != nil {
				slog.Warn("could not remove an old staged update", "file", f, "error", err)
				continue
			}
			slog.Info("removed an old staged update from the folder shared with Ollama", "file", f)
			_ = os.Remove(filepath.Dir(f)) // only when nothing else is in it
		}
	}
	_ = os.Remove(stage)

	if backupRoot != "" {
		backup := filepath.Join(legacyDir, "backup")
		own := filepath.Join(backup, backupRoot)
		if _, err := os.Stat(own); err == nil {
			upgraded = true
			if err := os.RemoveAll(own); err != nil {
				slog.Warn("could not remove the previous app's backup", "backup", own, "error", err)
			} else {
				slog.Info("removed the previous app's backup from the folder shared with Ollama", "backup", own)
			}
			if os.Remove(backup) == nil {
				// Nothing of the other app's was in it, so the marker beside it
				// is the one our own upgrade wrote.
				_ = os.Remove(filepath.Join(legacyDir, "upgraded"))
			}
		}
	}
	_ = os.Remove(legacyDir)
	return upgraded
}

// markUpgraded leaves the marker a finished upgrade leaves, for an upgrade an
// older build performed (see purgeLegacyStage).
func markUpgraded(marker string) {
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		slog.Warn("unable to create marker directory", "file", marker, "error", err)
		return
	}
	f, err := os.OpenFile(marker, os.O_RDONLY|os.O_CREATE, 0o666)
	if err != nil {
		slog.Warn("unable to create marker file", "file", marker, "error", err)
		return
	}
	f.Close()
}
