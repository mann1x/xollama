# windows-kvarn-cold.ps1 -- bug-4328 through xollama: a KVarN cache (kvarn3) on
# Vulkan at a COLD first boot. opencoti c14 r5 to r7 died there (0xC0000005 or
# 0xC00000FD, no message): a thread of ggml-vulkan.dll overflowed its 64 KiB
# stack while the NVIDIA shader compiler built the KVarN pipeline. A warm cache
# hides it, so both are made cold: opencoti's pipeline cache is switched off and
# the NVIDIA driver's shader disk cache is pointed at an empty directory.
# Run with -File on eleven2go; a side server on 22498, never the installed 22434.
param(
  [string]$Xollama = "$env:LOCALAPPDATA\Programs\xOllama\xollama.exe",
  [string]$Label = "arm",
  [string]$Model = "qwen3:8b",
  [string]$Dir = "$env:USERPROFILE\xollama-gate"
)
$ErrorActionPreference = 'Continue'
New-Item -ItemType Directory -Force -Path $Dir | Out-Null
$x = $Xollama; $u = 'http://127.0.0.1:22498'; $log = "$Dir\kvarn-cold-$Label.log"
"== $Label : $(& $x --version 2>&1 | Select-Object -Last 1)"
$nv = "$Dir\nvcache-$Label"; if (Test-Path $nv) { Remove-Item -Recurse -Force $nv }; New-Item -ItemType Directory -Force -Path $nv | Out-Null
Remove-Item Env:XOLLAMA_ENGINE_ARGS, Env:XOLLAMA_ENGINE, Env:XOLLAMA_ENGINE_PATH -ErrorAction SilentlyContinue
$env:XOLLAMA_HOST = '127.0.0.1:22498'; $env:OLLAMA_DEBUG = '1'; $env:OLLAMA_KEEP_ALIVE = '5m'
$env:OLLAMA_KV_CACHE_TYPE = 'kvarn3'; $env:OLLAMA_FLASH_ATTENTION = '1'
$env:OPENCOTI_VK_PIPELINE_CACHE = '0'
$env:__GL_SHADER_DISK_CACHE = '1'; $env:__GL_SHADER_DISK_CACHE_PATH = $nv; $env:__GL_SHADER_DISK_CACHE_SKIP_CLEANUP = '1'
$p = Start-Process -FilePath $x -ArgumentList 'serve' -RedirectStandardOutput "$log.out" -RedirectStandardError $log -PassThru -WindowStyle Hidden
$up = $false; foreach ($i in 1..60) { try { Invoke-RestMethod "$u/api/version" -TimeoutSec 2 | Out-Null; $up = $true; break } catch { Start-Sleep 1 } }
if (-not $up) { "  FAIL: side server did not start"; Stop-Process -Id $p.Id -Force; exit 1 }
$tag = "kvcold/$Label-vk3090"
& $x cp $Model $tag 2>&1 | Out-Null
& $x tweak model $tag '--device-backend=Vulkan' '--devices=name:NVIDIA GeForce RTX 3090' 2>&1 | Select-Object -Last 1
$body = @{ model = $tag; prompt = 'Write a long story about a lighthouse keeper.'; stream = $false; think = $false; options = @{ num_predict = 256; seed = 7; num_ctx = 8192 } } | ConvertTo-Json -Depth 4
try {
  $r = Invoke-RestMethod "$u/api/generate" -Method Post -Body $body -ContentType 'application/json' -TimeoutSec 600
  "  generate: eval_count=$($r.eval_count) tok/s=$([math]::Round($r.eval_count / ($r.eval_duration / 1e9), 1))"
} catch { "  generate FAILED: $($_.Exception.Message)" }
foreach ($pat in 'KVarN buffer size', 'pipeline cache', 'model placement', 'opencoti: fatal', 'exit status', '0xc0000005', '0xc00000fd', 'runner terminated', 'error loading model') {
  $m = Select-String -Path $log -Pattern $pat -SimpleMatch -ErrorAction SilentlyContinue | Select-Object -First 1
  if ($m) { "  [$pat] " + ($m.Line.Trim() -replace '^(.{0,260}).*$', '$1') }
}
try { Invoke-RestMethod "$u/api/generate" -Method Post -Body (@{ model = $tag; keep_alive = 0 } | ConvertTo-Json) -ContentType 'application/json' -TimeoutSec 60 | Out-Null } catch {}
Start-Sleep 2; & $x rm $tag 2>&1 | Out-Null
# The engine may still hold a cache file for a moment after the server stops.
Stop-Process -Id $p.Id -Force; Start-Sleep 5; Remove-Item -Recurse -Force $nv -ErrorAction SilentlyContinue
"  22434 listening: $([bool](Get-NetTCPConnection -State Listen -LocalPort 22434 -EA SilentlyContinue))  11434 listening: $([bool](Get-NetTCPConnection -State Listen -LocalPort 11434 -EA SilentlyContinue))"
