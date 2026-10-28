---
paths:
  - cmd/launch/xollama_noconsole.go
  - cmd/launch/xollama_noconsole_other.go
  - cmd/launch/xollama_noconsole_windows.go
  - cmd/launch/xollama_noconsole_test.go
  - cmd/launch/xollama_noconsole_windows_test.go
  - cmd/launch/claude_desktop.go
  - cmd/launch/codex_app.go
  - cmd/launch/vscode.go
  - docs/features/windows-installer.md
---

# PowerShell in `cmd/launch` runs without a console window

- **Every `exec.Command`/`exec.CommandContext` of PowerShell in `cmd/launch`
  goes through `noConsole(...)`**, marked `// xollama-hook: no-console`
  (Registry row in `docs/protocols/UPSTREAM-SYNC.md`). The desktop app is a GUI
  process with no console; when its UI lists the integrations it runs the
  Codex, Claude Desktop and VS Code checks, and on Windows each bare
  PowerShell opened a visible window at every start (owner, 2026-10-28).
- `noConsole` is additive (`cmd/launch/xollama_noconsole.go`):
  `xollama_noconsole_windows.go` sets `HideWindow` and ORs in
  `CREATE_NO_WINDOW` (keeping the caller's flags), `xollama_noconsole_other.go`
  does nothing, so Linux and macOS are upstream's.
- **What it hides is the PowerShell, not the app it opens.** `Start-Process`
  inside the script still opens Claude Desktop or Codex normally.
- **Not wrapped on purpose:** the interactive installers (`claude.go`,
  `qwen.go`, `kimi.go` return `"powershell"` as a command name; `hermes.go`
  runs it attached) and the CLI speech player keep their terminal.
- **After a sync,** `TestEveryLauncherPowerShellRunsWithoutAConsole` fails on
  any new upstream PowerShell call in the package: wrap it, one line, with the
  marker. `TestNoConsoleHidesTheWindowAndKeepsOtherFlags` (Windows) holds the
  flags.
- Prove a change on a real Windows desktop, not only the unit tests: while the
  app starts, watch for VISIBLE console windows from inside the logged-in
  session (an ssh session cannot see that desktop's windows). A process trace
  is not enough: with `CREATE_NO_WINDOW` PowerShell still gets a `conhost.exe`,
  only without a window. The probe used: `scripts/gates/windows-console-probe.ps1`.
- Prose: `docs/features/windows-installer.md`, "No console windows from the
  desktop app".

```sh
go test ./cmd/launch/...
```
