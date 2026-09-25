package onboarding

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// stockAppState matches a path into a stock ollama's own Windows state: its
// %LOCALAPPDATA%\Ollama directory, its install directory, or its login
// shortcut. xOllama is installed beside a stock ollama the user depends on, and
// each of these is a file the two would both claim. The first one found in the
// field: a stock server holding %LOCALAPPDATA%\Ollama\server.log open kept the
// xOllama app's server from starting at all.
var stockAppState = regexp.MustCompile(`(?i)` +
	`(LOCALAPPDATA"\)|localAppData)\s*,\s*("Programs"\s*,\s*)?"Ollama"` +
	`|"(Startup|lib)"\s*,\s*"Ollama\.lnk"`)

// The paths live in Windows-only files that this platform does not compile, so
// the guard reads the source. It covers every non-test Go file under app/ and
// internal/, so an upstream merge that brings a stock path back fails here
// rather than on someone's desktop.
func TestTheWindowsAppKeepsItsStateOutOfAStockOllamasDirectories(t *testing.T) {
	root := filepath.Join("..", "..")
	var found []string
	for _, dir := range []string{"app", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(src), "\n") {
				if stockAppState.MatchString(line) {
					found = append(found, filepath.ToSlash(path)+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range found {
		t.Errorf("stock ollama app state: %s", f)
	}
}

func TestTheStockAppStatePatternMatchesWhatItGuardsAgainst(t *testing.T) {
	for _, line := range []string{
		`appLogPath = filepath.Join(os.Getenv("LOCALAPPDATA"), "Ollama", "app.log")`,
		`return filepath.Join(localAppData, "Ollama", "db.sqlite")`,
		`appPath = filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Ollama")`,
		`startupShortcut = filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "Ollama.lnk")`,
		`shortcutOrigin := filepath.Join(appPath, "lib", "Ollama.lnk")`,
	} {
		if !stockAppState.MatchString(line) {
			t.Errorf("pattern misses %s", line)
		}
	}
	for _, line := range []string{
		`appLogPath = filepath.Join(os.Getenv("LOCALAPPDATA"), "xOllama", "app.log")`,
		`return filepath.Join(localAppData, "xOllama", "db.sqlite")`,
		`startupShortcut = filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "xOllama.lnk")`,
	} {
		if stockAppState.MatchString(line) {
			t.Errorf("pattern flags xOllama's own path %s", line)
		}
	}
}
