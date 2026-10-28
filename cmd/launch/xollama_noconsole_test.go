package launch

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every PowerShell the launchers start goes through noConsole: the desktop app
// calls these checks at every start, and on Windows one started bare opens a
// console window (zns, 2026-10-28: xOllama app.exe -> powershell.exe ->
// conhost.exe, 2.4 s after start). The interactive installers (claude, qwen,
// kimi, hermes) pass "powershell" as a name to an attached command and are not
// matched here.
func TestEveryLauncherPowerShellRunsWithoutAConsole(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	call := regexp.MustCompile(`exec\.Command(?:Context)?\((?:ctx, )?"powershell(?:\.exe)?"`)
	found := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, loc := range call.FindAllStringIndex(src, -1) {
			found++
			if !strings.HasSuffix(src[:loc[0]], "noConsole(") {
				line := strings.Count(src[:loc[0]], "\n") + 1
				t.Errorf("%s:%d: a PowerShell command not wrapped in noConsole opens a console window under the desktop app", name, line)
			}
		}
	}
	if found == 0 {
		t.Fatal("found no PowerShell commands at all; the pattern no longer matches the launchers")
	}
}
