package updater

// Which installer an update needs on Windows.
//
// The download is ~1.5 GB and almost none of it is what changed. Measured on
// the payload this fork ships: `lib\ollama` is 1.5 GB -- the opencoti artifact
// is ~700 MB of it and cuda_v13 ~785 MB, most of that cuBLAS -- against 36 MB
// for the Go binary. A release that only moves Go code therefore costs the user
// forty times the bytes it changed.
//
// So the release publishes two installers. xOllamaSetup.exe carries everything
// and is what a first install and a payload change need. xOllamaUpdate.exe
// carries the executables and nothing under lib\ollama, and refuses to run
// unless a matching payload is already on disk -- see the CORE branch of
// app/xollama.iss. Which one an update needs is decided by comparing the
// payload the installed build has against the payload the release was built
// with, and both sides of that comparison are the same digest: the full
// installer writes it to lib\ollama\PAYLOAD_ID, and the release publishes it as
// payload-id.txt.
//
// The identity is over the payload the INSTALLER SHIPS, not over the build
// tree, so the excluded backends (cuda_v12, mlx_*) do not move it. See
// payloadId in scripts/build_windows.ps1.

import (
	"os"
	"path/filepath"
)

func init() {
	// xOllamaUpdate.exe, beside xOllamaSetup.exe in the same release.
	CoreInstaller = "xOllamaUpdate.exe"
	InstalledPayloadID = windowsInstalledPayloadID
}

// payloadIDFile sits inside the payload it identifies, so it cannot be left
// behind by an uninstall that removed the payload, and a hand-extracted zip
// that has no such file simply reads as "unknown" and takes the full installer.
const payloadIDFile = "PAYLOAD_ID"

func windowsInstalledPayloadID() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(exe), "lib", "ollama", payloadIDFile))
	if err != nil {
		// No marker is not an error: an install from before this existed, or a
		// zip extraction, reads as unknown and gets the whole installer.
		return ""
	}
	return normalisePayloadID(string(raw))
}
