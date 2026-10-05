$i = "$env:LOCALAPPDATA\Programs\xOllama"; $x = "$i\xollama-c8.exe"; $d = "$env:USERPROFILE\xollama-c8"; $u = 'http://127.0.0.1:22498'; $log = "$d\serve-w140.log"
$eng = "$d\engines\opencoti-llamafile-0.10.5-c8-win-x86_64.llamafile.exe"
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
& $x cp qwen3:8b rc1/qwen3-igpu 2>&1 | Out-Null; & $x cp qwen3:8b rc1/qwen3-vk9070 2>&1 | Out-Null
& $x tweak model rc1/qwen3-igpu '--device-backend=Vulkan' '--devices=integrated' 2>&1 | Select-Object -Last 1
& $x tweak model rc1/qwen3-vk9070 '--device-backend=Vulkan' '--devices=name:AMD Radeon RX 9070 XT' 2>&1 | Select-Object -Last 1
function Gen($tag, $n, $seed) { $b = @{ model = $tag; prompt = 'Write a detailed 1500-word essay on the history of the steam engine, from Newcomen to the end of the age of steam. Do not stop early.'; stream = $false; think = $false; options = @{ num_predict = $n; seed = $seed } } | ConvertTo-Json -Depth 5
  try { $g = Invoke-RestMethod "$u/api/generate" -Method Post -ContentType 'application/json' -Body $b -TimeoutSec 900; '{0} tok {1:N1} tok/s ({2}) load {3:N1}s' -f $g.eval_count, ($g.eval_count / ($g.eval_duration / 1e9)), $g.done_reason, ($g.load_duration / 1e9) } catch { "FAILED: $($_.ErrorDetails.Message) $($_.Exception.Message)" } }
function Unload($tag) { try { Invoke-RestMethod "$u/api/generate" -Method Post -ContentType 'application/json' -Body (@{ model = $tag; keep_alive = 0 } | ConvertTo-Json) -TimeoutSec 60 | Out-Null } catch {}; for ($n = 0; $n -lt 40; $n++) { if (@(Get-Process | Where-Object { $_.Path -like "$d\engines\*" }).Count -eq 0) { break }; Start-Sleep -Milliseconds 500 } }
function Place { $l = (Get-Content "$log.err" | Select-String 'model placement' | Select-Object -Last 1).Line; ($l -replace '^.*devices=', 'devices=').Substring(0, 60) }
"== B. RTX 3090 CUDA, qwen3:8b, 512 tokens"
0..3 | ForEach-Object { "  run $_ : $(Gen 'qwen3:8b' 512 (7 + $_))" }; "  placement $(Place)"; Unload 'qwen3:8b'
"== C. RX 9070 XT Vulkan (pin by name), 512 tokens"
0..3 | ForEach-Object { "  run $_ : $(Gen 'rc1/qwen3-vk9070' 512 (7 + $_))" }; "  placement $(Place)"
"  /props and /kv with the model loaded:"
foreach ($ep in 'props', 'kv', 'props', 'kv') { try { $r = Invoke-WebRequest "$u/api/engine?model=rc1/qwen3-vk9070&endpoint=$ep" -UseBasicParsing -TimeoutSec 30; "    $ep $($r.StatusCode) $($r.Content.Length) bytes" } catch { "    $ep FAILED $($_.Exception.Message)" } }
"  idle 120 s with the model loaded"; Start-Sleep 120
"  after idle: $(Gen 'rc1/qwen3-vk9070' 256 21)   card $(Card) dumps +$((Dumps) - $d0)"
"== E. server killed with the model loaded on the 9070 XT"
$e = @(Get-Process | Where-Object { $_.Path -like "$d\engines\*" }); "  engines before: $($e.Count) (pid $($e.Id -join ','))"
Stop-Process -Id $p.Id -Force; $sw = [Diagnostics.Stopwatch]::StartNew()
while ($sw.Elapsed.TotalSeconds -lt 20 -and @(Get-Process | Where-Object { $_.Path -like "$d\engines\*" }).Count -gt 0) { Start-Sleep -Milliseconds 200 }
"  engines left $(@(Get-Process | Where-Object { $_.Path -like "$d\engines\*" }).Count) after $([int]$sw.Elapsed.TotalMilliseconds) ms"
Start-Sleep 40; "  40 s later: card $(Card) dumps +$((Dumps) - $d0)"
"== D. Radeon iGPU Vulkan (pin integrated), 256 tokens, new server"
$p = Start-Process -FilePath $x -ArgumentList 'serve' -WindowStyle Hidden -RedirectStandardOutput "$log.2" -RedirectStandardError "$log.2.err" -PassThru
$ok = $false; for ($n = 0; $n -lt 60 -and -not $ok; $n++) { Start-Sleep 1; try { [void](Invoke-RestMethod "$u/api/version" -TimeoutSec 2); $ok = $true } catch {} }
0..1 | ForEach-Object { "  run $_ : $(Gen 'rc1/qwen3-igpu' 256 (7 + $_))" }
$l = (Get-Content "$log.2.err" | Select-String 'model placement' | Select-Object -Last 1).Line; "  placement " + ($l -replace '^.*devices=', 'devices=').Substring(0, 60)
Get-Content "$log.2.err" | Select-String 'offloaded \d+/\d+' | Select-Object -Last 1 | ForEach-Object { "  $($_.Line.Trim())" }
Unload 'rc1/qwen3-igpu'; Stop-Process -Id $p.Id -Force
"== engine and library lines"
Get-Content "$log.err" | Select-String 'opencoti build', 'loaded .*ggml-', 'abi ' | Select-Object -Unique -First 8 | ForEach-Object { "  " + $_.Line.Substring(0, [math]::Min(210, $_.Line.Length)) }
"end: card $(Card) dumps +$((Dumps) - $d0); 22434 listening $([bool](Get-NetTCPConnection -State Listen -LocalPort 22434 -ErrorAction SilentlyContinue)); 11434 listening $([bool](Get-NetTCPConnection -State Listen -LocalPort 11434 -ErrorAction SilentlyContinue))"
