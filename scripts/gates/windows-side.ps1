# G8 on eleven2go: a side server on 22498 beside the installed xOllama, on the
# engine the install carries (the pinned one). Copy it over and run it in the
# logged-in session (an ssh session cannot start GPU engines):
#   scp scripts/gates/windows-side.ps1 scripts/gates/windows-gate-run.ps1 eleven2go:C:/Users/ManniX/xollama-gate/
#   ssh eleven2go "powershell -NoProfile -ExecutionPolicy Bypass -File C:\Users\ManniX\xollama-gate\windows-gate-run.ps1 -Script C:\Users\ManniX\xollama-gate\windows-side.ps1 -Out C:\Users\ManniX\xollama-gate\g8.out"
# Engines are counted by parent process, so the installed server's own engine
# never counts. The rc1/ tags it makes are removed at the end.
param(
  [string]$Xollama = "$env:LOCALAPPDATA\Programs\xOllama\xollama.exe",
  [string]$Engine = '',
  [string]$Dir = "$env:USERPROFILE\xollama-gate"
)
$i = Split-Path $Xollama; $x = $Xollama; $d = $Dir; $u = 'http://127.0.0.1:22498'; $log = "$d\serve-g8.log"
if (-not $Engine) { $f = @(Get-ChildItem "$i\lib\ollama\engines" -Filter 'opencoti*.exe'); if ($f.Count -ne 1) { "ABORT: expected one engine in $i\lib\ollama\engines, found $($f.Count)"; exit 1 }; $Engine = $f[0].FullName }
$eng = $Engine; New-Item -ItemType Directory -Force $d | Out-Null
"server $(& $x --version 2>&1 | Select-Object -Last 1); engine $(Split-Path $eng -Leaf)"
function Engines { @(Get-CimInstance Win32_Process -Filter "ParentProcessId=$($script:p.Id)" | Where-Object { $_.Name -like 'opencoti*' }) }
function Card { (Get-PnpDevice -Class Display -PresentOnly | Where-Object FriendlyName -match '9070').Status }
function Dumps { @(Get-ChildItem C:\Windows\LiveKernelReports -Recurse -File -Filter *.dmp -ErrorAction SilentlyContinue).Count }
$d0 = Dumps
"== A. device ids (OPENCOTI_LIST_DEVICE_IDS=1)"
$env:OPENCOTI_LIST_DEVICE_IDS = '1'
foreach ($g in 'vulkan', 'nvidia') { & $eng --server --list-devices -m nonexistent.gguf --offline --gpu $g 2>&1 | ForEach-Object { "$_" } | Select-String '^\s+(Vulkan|CUDA)\d+:' | ForEach-Object { "  $($_.Line.Trim())" } }
Remove-Item Env:OPENCOTI_LIST_DEVICE_IDS
if (Get-NetTCPConnection -State Listen -LocalPort 22498 -ErrorAction SilentlyContinue) { 'ABORT: 22498 in use'; exit 1 }
Remove-Item Env:XOLLAMA_ENGINE_ARGS, Env:XOLLAMA_ENGINE -ErrorAction SilentlyContinue
$env:XOLLAMA_HOST = '127.0.0.1:22498'; $env:XOLLAMA_ENGINE_PATH = $eng; $env:OLLAMA_DEBUG = '1'; $env:OLLAMA_KEEP_ALIVE = '30m'
Remove-Item $log, "$log.err" -ErrorAction SilentlyContinue
$p = Start-Process -FilePath $x -ArgumentList 'serve' -WindowStyle Hidden -RedirectStandardOutput $log -RedirectStandardError "$log.err" -PassThru
$ok = $false; for ($n = 0; $n -lt 60 -and -not $ok; $n++) { Start-Sleep 1; try { [void](Invoke-RestMethod "$u/api/version" -TimeoutSec 2); $ok = $true } catch {} }
(Invoke-RestMethod "$u/api/xollama/devices" -TimeoutSec 60).devices | ForEach-Object { "  device {0} #{1} pci='{2}' {3} -> {4}" -f $_.backend, $_.id, $_.pci_id, $_.description, $_.engine }
# Every part pins its GPU: an unpinned model follows the host's gpu policy
# (xollama-settings.json), which on eleven2go fills the 9070 XT first.
$pol = "$env:USERPROFILE\.ollama\xollama-settings.json"; if (Test-Path $pol) { "  server settings: $(Get-Content $pol -Raw)" }
& $x cp qwen3:8b rc1/qwen3-igpu 2>&1 | Out-Null; & $x cp qwen3:8b rc1/qwen3-vk9070 2>&1 | Out-Null; & $x cp qwen3:8b rc1/qwen3-cuda3090 2>&1 | Out-Null
& $x tweak model rc1/qwen3-cuda3090 '--device-backend=CUDA' '--devices=name:NVIDIA GeForce RTX 3090' 2>&1 | Select-Object -Last 1
& $x tweak model rc1/qwen3-igpu '--device-backend=Vulkan' '--devices=integrated' 2>&1 | Select-Object -Last 1
& $x tweak model rc1/qwen3-vk9070 '--device-backend=Vulkan' '--devices=name:AMD Radeon RX 9070 XT' 2>&1 | Select-Object -Last 1
function Gen($tag, $n, $seed) { $b = @{ model = $tag; prompt = 'Write a detailed 1500-word essay on the history of the steam engine, from Newcomen to the end of the age of steam. Do not stop early.'; stream = $false; think = $false; options = @{ num_predict = $n; seed = $seed } } | ConvertTo-Json -Depth 5
  try { $g = Invoke-RestMethod "$u/api/generate" -Method Post -ContentType 'application/json' -Body $b -TimeoutSec 900; '{0} tok {1:N1} tok/s ({2}) load {3:N1}s' -f $g.eval_count, ($g.eval_count / ($g.eval_duration / 1e9)), $g.done_reason, ($g.load_duration / 1e9) } catch { "FAILED: $($_.ErrorDetails.Message) $($_.Exception.Message)" } }
function Unload($tag) { try { Invoke-RestMethod "$u/api/generate" -Method Post -ContentType 'application/json' -Body (@{ model = $tag; keep_alive = 0 } | ConvertTo-Json) -TimeoutSec 60 | Out-Null } catch {}; for ($n = 0; $n -lt 40; $n++) { if ((Engines).Count -eq 0) { break }; Start-Sleep -Milliseconds 500 } }
function Cut([string]$s, [int]$n = 60) { if ($s.Length -gt $n) { $s.Substring(0, $n) } else { $s } }
function Place { $l = (Get-Content "$log.err" | Select-String 'model placement' | Select-Object -Last 1).Line; Cut ($l -replace '^.*devices=', 'devices=') }
"== B. RTX 3090 CUDA, qwen3:8b, 512 tokens"
0..3 | ForEach-Object { "  run $_ : $(Gen 'rc1/qwen3-cuda3090' 512 (7 + $_))" }; "  placement $(Place)"; Unload 'rc1/qwen3-cuda3090'
"== C. RX 9070 XT Vulkan (pin by name), 512 tokens"
0..3 | ForEach-Object { "  run $_ : $(Gen 'rc1/qwen3-vk9070' 512 (7 + $_))" }; "  placement $(Place)"
"  /props and /kv with the model loaded:"
foreach ($ep in 'props', 'kv', 'props', 'kv') { try { $r = Invoke-WebRequest "$u/api/engine?model=rc1/qwen3-vk9070&endpoint=$ep" -UseBasicParsing -TimeoutSec 30; "    $ep $($r.StatusCode) $($r.Content.Length) bytes" } catch { "    $ep FAILED $($_.Exception.Message)" } }
"  idle 120 s with the model loaded"; Start-Sleep 120
"  after idle: $(Gen 'rc1/qwen3-vk9070' 256 21)   card $(Card) dumps +$((Dumps) - $d0)"
"== E. server killed with the model loaded on the 9070 XT"
$e = @(Engines); "  engines before: $($e.Count) (pid $($e.ProcessId -join ','))"
Stop-Process -Id $p.Id -Force; $sw = [Diagnostics.Stopwatch]::StartNew()
while ($sw.Elapsed.TotalSeconds -lt 20 -and @($e | Where-Object { Get-Process -Id $_.ProcessId -ErrorAction SilentlyContinue }).Count -gt 0) { Start-Sleep -Milliseconds 200 }
"  engines left $(@($e | Where-Object { Get-Process -Id $_.ProcessId -ErrorAction SilentlyContinue }).Count) after $([int]$sw.Elapsed.TotalMilliseconds) ms"
Start-Sleep 40; "  40 s later: card $(Card) dumps +$((Dumps) - $d0)"
"== D. Radeon iGPU Vulkan (pin integrated), 256 tokens, new server"
$p = Start-Process -FilePath $x -ArgumentList 'serve' -WindowStyle Hidden -RedirectStandardOutput "$log.2" -RedirectStandardError "$log.2.err" -PassThru
$ok = $false; for ($n = 0; $n -lt 60 -and -not $ok; $n++) { Start-Sleep 1; try { [void](Invoke-RestMethod "$u/api/version" -TimeoutSec 2); $ok = $true } catch {} }
0..1 | ForEach-Object { "  run $_ : $(Gen 'rc1/qwen3-igpu' 256 (7 + $_))" }
$l = (Get-Content "$log.2.err" | Select-String 'model placement' | Select-Object -Last 1).Line; "  placement " + (Cut ($l -replace '^.*devices=', 'devices='))
Get-Content "$log.2.err" | Select-String 'offloaded \d+/\d+' | Select-Object -Last 1 | ForEach-Object { "  $($_.Line.Trim())" }
Unload 'rc1/qwen3-igpu'; & $x rm rc1/qwen3-igpu rc1/qwen3-vk9070 rc1/qwen3-cuda3090 2>&1 | Out-Null; "  rc1/ tags removed"; Stop-Process -Id $p.Id -Force
"== engine and library lines"
Get-Content "$log.err" | Select-String 'opencoti build', 'loaded .*ggml-', 'abi ' | Select-Object -Unique -First 8 | ForEach-Object { "  " + $_.Line.Substring(0, [math]::Min(210, $_.Line.Length)) }
"end: card $(Card) dumps +$((Dumps) - $d0); 22434 listening $([bool](Get-NetTCPConnection -State Listen -LocalPort 22434 -ErrorAction SilentlyContinue)); 11434 listening $([bool](Get-NetTCPConnection -State Listen -LocalPort 11434 -ErrorAction SilentlyContinue))"
