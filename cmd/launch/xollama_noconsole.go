package launch

import "os/exec"

// noConsole starts cmd without a console window of its own (xollama-hook:
// no-console). The desktop app is a GUI process with no console, so on Windows
// every PowerShell it runs to look for Codex, Claude Desktop or VS Code opened
// a visible console window, the blue window at every start. Elsewhere it
// returns cmd unchanged.
func noConsole(cmd *exec.Cmd) *exec.Cmd {
	hideConsole(cmd)
	return cmd
}
