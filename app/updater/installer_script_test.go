package updater

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Inno Setup creates {app} only once the wizard has an install directory.
// InitializeSetup and InitializeWizard run before that, and a script that
// expands it there ends with a runtime error instead of installing: the
// update installer of v0.35.1-xollama did, on every machine.
func TestTheInstallerNeverAsksForAppBeforeItExists(t *testing.T) {
	early := regexp.MustCompile(`(?ms)^(?:function|procedure) (InitializeSetup|InitializeWizard)\b.*?^end;`)
	for _, name := range []string{"../xollama.iss", "../xollama-setup-pages.iss", "../xollama-mlx.iss"} {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range early.FindAllStringSubmatch(string(b), -1) {
			if strings.Contains(m[0], "{app}") {
				t.Errorf("%s: %s expands {app}, which does not exist yet", name, m[1])
			}
		}
	}
}

// The update-only installer carries no engine, so it must still refuse a
// machine whose payload is not the one it was built for, at the first point
// where the install directory is known and before anything is stopped.
func TestTheUpdateInstallerStillChecksThePayload(t *testing.T) {
	b, err := os.ReadFile("../xollama.iss")
	if err != nil {
		t.Fatal(err)
	}
	prepare := regexp.MustCompile(`(?ms)^function PrepareToInstall\b.*?^end;`).FindString(string(b))
	refuse := strings.Index(prepare, "PayloadRefusal()")
	stop := strings.Index(prepare, "StopXollama(")
	if refuse < 0 || stop < 0 || refuse > stop {
		t.Fatalf("PrepareToInstall must ask PayloadRefusal before it stops xOllama:\n%s", prepare)
	}
}

// MLX is downloaded, not carried (owner, 2026-10-07; 2 GiB asset cap): the
// download must name the pinned sha256, both installers must run it after
// their files are in place, and the full installer must move a current MLX
// aside only after xOllama is stopped (its DLLs are in use while it runs).
func TestBothInstallersFetchThePinnedMLX(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	mlx, main, pages := read("../xollama-mlx.iss"), read("../xollama.iss"), read("../xollama-setup-pages.iss")
	if !regexp.MustCompile(`DownloadTemporaryFile\('\{#PKG_MLX_URL\}', '[^']+', '\{#PKG_MLX_SHA256\}'`).MatchString(mlx) {
		t.Error("xollama-mlx.iss: the download must be checked against PKG_MLX_SHA256")
	}
	if !strings.Contains(main, `#include "xollama-mlx.iss"`) {
		t.Error("xollama.iss does not include xollama-mlx.iss")
	}
	if !strings.Contains(regexp.MustCompile(`(?ms)^procedure CurStepChanged\b.*?^end;`).FindString(pages), "InstallMLX()") {
		t.Error("the full installer's CurStepChanged (xollama-setup-pages.iss) does not call InstallMLX")
	}
	core := regexp.MustCompile(`(?ms)^#ifdef CORE\s*\n// The full installer calls InstallMLX.*?^#endif`).FindString(main)
	if !strings.Contains(core, "InstallMLX()") {
		t.Error("the update installer has no CurStepChanged calling InstallMLX")
	}
	prepare := regexp.MustCompile(`(?ms)^function PrepareToInstall\b.*?^end;`).FindString(main)
	keep, stop := strings.Index(prepare, "KeepMLX()"), strings.Index(prepare, "StopXollama(")
	if keep < 0 || stop < 0 || keep < stop {
		t.Errorf("PrepareToInstall must call KeepMLX after StopXollama:\n%s", prepare)
	}
}
