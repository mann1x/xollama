# windows-mlock.ps1 -- opencoti 0835 through xollama: --mlock on Windows. Until c14 r8
# the engine never raised the process working set, so every host buffer logged
# "failed to mlock ... (win32 error 1453)" and the load went on unlocked. The
# operator's flags reach the engine through XOLLAMA_ENGINE_ARGS; qwen3:8b with
# num_gpu 8 on the RTX 3090 (CUDA) leaves most weights in host buffers.
# Run with -File on eleven2go; a side server on 22498, never the installed 22434.
param(
  [string]$Xollama = "$env:LOCALAPPDATA\Programs\xOllama\xollama.exe",
  [string]$Label = "arm",
  [string]$Dir = "$env:USERPROFILE\xollama-gate"
)
New-Item -ItemType Directory -Force -Path $Dir | Out-Null
$x = $Xollama; $u = 'http://127.0.0.1:22498'; $log = "$Dir\mlock-$Label.log"
"== $Label : $(& $x --version 2>&1 | Select-Object -Last 1)"
Remove-Item Env:XOLLAMA_ENGINE, Env:XOLLAMA_ENGINE_PATH, Env:OLLAMA_KV_CACHE_TYPE -ErrorAction SilentlyContinue
$env:XOLLAMA_HOST = '127.0.0.1:22498'; $env:OLLAMA_DEBUG = '1'; $env:OLLAMA_KEEP_ALIVE = '5m'
$env:XOLLAMA_ENGINE_ARGS = '--no-mmap --mlock'
$p = Start-Process -FilePath $x -ArgumentList 'serve' -RedirectStandardOutput "$log.out" -RedirectStandardError $log -PassThru -WindowStyle Hidden
$up = $false; foreach ($i in 1..60) { try { Invoke-RestMethod "$u/api/version" -TimeoutSec 2 | Out-Null; $up = $true; break } catch { Start-Sleep 1 } }
if (-not $up) { "  FAIL: side server did not start"; Stop-Process -Id $p.Id -Force; exit 1 }
$tag = "mlock/$Label-cuda3090"
& $x cp qwen3:8b $tag 2>&1 | Out-Null
& $x tweak model $tag '--device-backend=CUDA' '--devices=name:NVIDIA GeForce RTX 3090' 2>&1 | Select-Object -Last 1
$body = @{ model = $tag; prompt = 'Write a long story about a lighthouse keeper.'; stream = $false; think = $false; options = @{ num_predict = 256; seed = 7; num_ctx = 4096; num_gpu = 8 } } | ConvertTo-Json -Depth 4
try {
  $r = Invoke-RestMethod "$u/api/generate" -Method Post -Body $body -ContentType 'application/json' -TimeoutSec 600
  "  generate: eval_count=$($r.eval_count) tok/s=$([math]::Round($r.eval_count / ($r.eval_duration / 1e9), 1))"
} catch { "  generate FAILED: $($_.Exception.Message)" }
"  'failed to mlock' lines: $((Select-String -Path $log -Pattern 'failed to mlock' -SimpleMatch -EA SilentlyContinue | Measure-Object).Count)"
foreach ($pat in 'appending operator-supplied', 'failed to mlock', 'mlock:', 'locked', 'model placement', 'llama-server terminated') {
  Select-String -Path $log -Pattern $pat -SimpleMatch -ErrorAction SilentlyContinue | Select-Object -First 2 | ForEach-Object { "  [$pat] " + ($_.Line.Trim() -replace '^(.{0,240}).*$', '$1') }
}
try { Invoke-RestMethod "$u/api/generate" -Method Post -Body (@{ model = $tag; keep_alive = 0 } | ConvertTo-Json) -ContentType 'application/json' -TimeoutSec 60 | Out-Null } catch {}
Start-Sleep 2; & $x rm $tag 2>&1 | Out-Null
Stop-Process -Id $p.Id -Force
"  22434 listening: $([bool](Get-NetTCPConnection -State Listen -LocalPort 22434 -EA SilentlyContinue))  11434 listening: $([bool](Get-NetTCPConnection -State Listen -LocalPort 11434 -EA SilentlyContinue))"
