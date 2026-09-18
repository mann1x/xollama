package envconfig

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// bypass matches a direct read of one of our own variables. Var is what
// resolves the XOLLAMA_ spelling and trims quotes, so a read that goes around
// it silently ignores the override -- the variable appears in `xollama serve
// --help` under its XOLLAMA_ name and then does nothing.
var bypass = regexp.MustCompile(`os\.(Getenv|LookupEnv)\("X?OLLAMA_`)

// skipDirs are trees this rule does not reach: vendored upstream C/Go, build
// output, and the UI's node_modules.
var skipDirs = map[string]bool{
	".git":         true,
	"build":        true,
	"dist":         true,
	"llama":        true,
	"node_modules": true,
	"vendor":       true,
}

func TestNoDirectReadsOfOurOwnEnvironment(t *testing.T) {
	root := ".."
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Skipf("not running from a source tree: %v", err)
	}

	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if bypass.MatchString(line) {
				found = append(found, filepath.ToSlash(path)+":"+strings.TrimSpace(line)+" (line "+strconv.Itoa(i+1)+")")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	for _, f := range found {
		t.Errorf("read our environment directly, use envconfig.Var: %s", f)
	}
}

// The guard is only worth having if it matches; these are the exact forms it
// replaced.
func TestBypassPatternMatchesWhatItReplaced(t *testing.T) {
	for _, line := range []string{
		`		if debug := os.Getenv("OLLAMA_DEBUG"); debug != "" {`,
		`	forcedVariant, _ := os.LookupEnv("OLLAMA_LLM_LIBRARY")`,
		`	decision := Resolve(Host(), devices, os.Getenv("XOLLAMA_ENGINE"))`,
	} {
		if !bypass.MatchString(line) {
			t.Errorf("pattern missed %q", line)
		}
	}
	for _, line := range []string{
		`	localAppData := os.Getenv("LOCALAPPDATA")`,
		`		if numericVisibleDeviceList(os.Getenv(name)) {`,
		`		if v := trimVar(os.Getenv(x)); v != "" {`,
	} {
		if bypass.MatchString(line) {
			t.Errorf("pattern wrongly matched %q", line)
		}
	}
}
