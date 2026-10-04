$i = "$env:LOCALAPPDATA\Programs\xOllama"; $d = "$env:USERPROFILE\xollama-c8"; $u = 'http://127.0.0.1:22498'; $log = "$d\serve-speech.log"
Remove-Item Env:XOLLAMA_ENGINE_ARGS -ErrorAction SilentlyContinue
$env:XOLLAMA_ENGINE_PATH = "$d\engines\opencoti-llamafile-0.10.5-c8-win-x86_64.llamafile.exe"; $env:OLLAMA_MODELS = "$env:USERPROFILE\xollama-b120\models"; $env:XOLLAMA_HOST = '127.0.0.1:22498'
New-Item -ItemType Directory -Force "$d\out" | Out-Null; Remove-Item $log, "$log.err" -ErrorAction SilentlyContinue
$p = Start-Process -FilePath "$i\xollama.exe" -ArgumentList 'serve' -WindowStyle Hidden -RedirectStandardOutput $log -RedirectStandardError "$log.err" -PassThru
$ok = $false; for ($n = 0; $n -lt 60 -and -not $ok; $n++) { Start-Sleep 1; try { $v = (Invoke-RestMethod "$u/api/version" -TimeoutSec 2).version; $ok = $true } catch {} }
"models: $(((Invoke-RestMethod "$u/api/tags").models | ForEach-Object name) -join ', ')"
foreach ($m in 'mannix/kokoro:82m','mannix/kittentts:mini-0.8','mannix/supertonic:3','mannix/outetts:0.3') {
  $f = "$d\out\$($m -replace '[/:]','_').mp3"; $b = @{ model = $m; input = 'The first ship came in at dawn.' } | ConvertTo-Json
  try { Invoke-WebRequest "$u/v1/audio/speech" -Method Post -ContentType 'application/json' -Body $b -OutFile $f -TimeoutSec 600 -UseBasicParsing; $t = (& curl.exe -s -m 600 "$u/v1/audio/transcriptions" -F model=mannix/whisper:large-v3-turbo -F "file=@$f"); "$m -> $((Get-Item $f).Length) bytes -> $t" } catch { "$m FAILED: $($_.ErrorDetails.Message) $($_.Exception.Message)" }
}
Get-Content "$log.err" | Select-String -Pattern 'espeak: ','audiocpp: ','codec: ','opencoti build' | Select-Object -First 12 | ForEach-Object { "  log: $($_.Line.Substring(0, [math]::Min(200, $_.Line.Length)))" }
Stop-Process -Id $p.Id -Force; Start-Sleep 2
$x = "$i\xollama.exe"; Remove-Item Env:XOLLAMA_HOST, Env:OLLAMA_MODELS, Env:XOLLAMA_ENGINE_PATH
foreach ($t in 'rc1/qwen3-cuda','rc1/qwen3-vk9070','rc1/qwen3-vkigpu') { & $x rm $t 2>$null | Out-Null }
"left: $(((Invoke-RestMethod http://127.0.0.1:22434/api/tags).models | Where-Object { $_.name -match '^rc1/|^qwen3:8b' } | ForEach-Object name) -join ', ')"
Get-NetTCPConnection -State Listen -LocalPort 22434,11434,22498 -ErrorAction SilentlyContinue | ForEach-Object { "port $($_.LocalPort) pid $($_.OwningProcess)" }
"ollama 11434: $((Invoke-RestMethod http://127.0.0.1:11434/api/version -TimeoutSec 5).version); nvidia: $(nvidia-smi --query-gpu=memory.used --format=csv,noheader)"
