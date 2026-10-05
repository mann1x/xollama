package updater

import (
	"path/filepath"
	"testing"
)

func TestTheStagePathsAreNotUpstreams(t *testing.T) {
	for name, p := range map[string]string{"stage": UpdateStageDir, "marker": UpgradeMarkerFile, "backup": appBackupDir} {
		if got := filepath.Base(filepath.Dir(p)); got != forkCacheName {
			t.Errorf("%s is %s, under %q; want it under %q", name, p, got, forkCacheName)
		}
	}
}
