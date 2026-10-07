# Runs a gate script in the logged-in user's session (GPU engines need it; an
# ssh session cannot start them) and waits for it, then prints its output.
#   powershell -Command "& windows-gate-run.ps1 -Script <gate.ps1> -Out <file> [-Minutes 60] [-ScriptArgs '...']"
param(
  [Parameter(Mandatory)][string]$Script,
  [Parameter(Mandatory)][string]$Out,
  [string]$ScriptArgs = '',
  [int]$Minutes = 60
)
$ErrorActionPreference = 'Stop'
$name = 'xollama-gate-run'
$wrap = [IO.Path]::ChangeExtension($Out, '.cmd')
Remove-Item $Out -ErrorAction SilentlyContinue
# A .cmd wrapper, started by schtasks /IT: the pattern the install gates use.
Set-Content -Encoding ascii $wrap @"
@echo off
powershell -NoProfile -ExecutionPolicy Bypass -File "$Script" $ScriptArgs > "$Out" 2>&1
echo GATE-RUN-EXIT %ERRORLEVEL% >> "$Out"
"@
schtasks /Create /TN $name /TR "`"$wrap`"" /SC ONCE /ST 23:59 /IT /F | Out-Null
schtasks /Run /TN $name | Out-Null
# Wait for the wrapper's own exit line; the task's state is not reliable here.
function Done { (Test-Path $Out) -and (Select-String -Path $Out -Pattern '^GATE-RUN-EXIT' -Quiet) }
$end = (Get-Date).AddMinutes($Minutes)
do { Start-Sleep 5 } until ((Done) -or (Get-Date) -ge $end)
if (-not (Done)) { schtasks /End /TN $name | Out-Null; "TIMED OUT after $Minutes min" }
schtasks /Delete /TN $name /F | Out-Null
if (Test-Path $Out) { Get-Content $Out } else { "no output at $Out" }
