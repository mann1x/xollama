# Stops xOllama before its installer replaces files, and before its uninstaller
# removes them: the tray app, the server, and the engines and runners they
# started. A stock Ollama installed beside it is left running -- its tray, its
# server and its own llama-server.exe runners -- which a blanket
# `taskkill /im llama-server.exe` would have stopped too.
#
# What is xOllama's:
#   - any process whose executable is under the install directory (-App);
#   - any engine or runner (llama-server, opencoti-*, xollama) started by one of
#     those, wherever it runs from: XOLLAMA_ENGINE_PATH can point anywhere;
#   - an opencoti engine whose parent is gone. Only xOllama launches opencoti,
#     so an orphan is one a crashed or killed server left behind, still holding
#     its executable and the GPU. One started from a shell has a live parent
#     and is not touched.
# -List prints what would be stopped and stops nothing.
param([Parameter(Mandatory = $true)][string]$App, [int]$WaitSeconds = 20, [switch]$List)

$ErrorActionPreference = 'SilentlyContinue'
$root = [IO.Path]::GetFullPath($App).TrimEnd('\') + '\'
$engine = '^(llama-server|opencoti.*|xollama)\.exe$'

$all = @(Get-CimInstance Win32_Process)
$byId = @{}
foreach ($p in $all) { $byId[[int]$p.ProcessId] = $p }

function Under($path) { $path -and $path.StartsWith($root, [StringComparison]::OrdinalIgnoreCase) }

# A parent is only the parent if it started first: Windows reuses process ids.
function Orphan($p) {
  $parent = $byId[[int]$p.ParentProcessId]
  -not $parent -or ($parent.CreationDate -gt $p.CreationDate)
}

$stop = @{}
$queue = [Collections.Queue]::new()
foreach ($p in $all) { if (Under $p.ExecutablePath) { $queue.Enqueue($p) } }
while ($queue.Count) {
  $p = $queue.Dequeue()
  if ($stop.ContainsKey([int]$p.ProcessId)) { continue }
  $stop[[int]$p.ProcessId] = $p
  foreach ($c in $all) {
    if ($c.ParentProcessId -eq $p.ProcessId -and $c.Name -match $engine -and $c.CreationDate -ge $p.CreationDate) { $queue.Enqueue($c) }
  }
}
foreach ($p in $all) {
  if ($p.Name -like 'opencoti*' -and (Orphan $p)) { $stop[[int]$p.ProcessId] = $p }
}

foreach ($p in $stop.Values) {
  "{0} {1} ({2}) {3}" -f $(if ($List) { 'would stop' } else { 'stopping' }), $p.Name, $p.ProcessId, $p.ExecutablePath
  if (-not $List) { Stop-Process -Id $p.ProcessId -Force }
}
if ($List) { exit 0 }

# Wait until they are gone, so no file is still held open when the caller goes on.
$deadline = (Get-Date).AddSeconds($WaitSeconds)
do {
  $left = @($stop.Keys | Where-Object { Get-Process -Id $_ })
  if (-not $left.Count) { break }
  Start-Sleep -Milliseconds 250
} while ((Get-Date) -lt $deadline)
if ($left.Count) { "still running: $($left -join ', ')"; exit 1 }
exit 0
