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
	for _, name := range []string{"../xollama.iss", "../xollama-setup-pages.iss"} {
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
