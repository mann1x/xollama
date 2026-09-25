//go:build windows

package updater

import (
	"os"
	"path/filepath"
	"testing"
)

// The fork's update channel is unsigned. verifyDownload reports a missing or
// unrecognised signature and accepts the installer anyway, because the gate that
// tells our installer from another product's is the sha256 the release
// published (forkDigest, before this runs). Upstream's rejection tests are
// skipped for that reason; this holds the fork's side. When the fork gets a
// certificate, the update-signer block and this test change together.
func TestVerifyDownloadAcceptsAnUnsignedInstallerOnTheForkChannel(t *testing.T) {
	oldUpdateStageDir := UpdateStageDir
	defer func() { UpdateStageDir = oldUpdateStageDir }()

	t.Setenv("LOCALAPPDATA", t.TempDir())
	UpdateStageDir = t.TempDir()
	bundle := filepath.Join(UpdateStageDir, "etag", "xOllamaSetup.exe")
	if err := os.MkdirAll(filepath.Dir(bundle), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundle, []byte("not a signed installer"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := verifyDownload(); err != nil {
		t.Fatalf("an unsigned installer must be accepted on the fork channel, got %v", err)
	}
}
