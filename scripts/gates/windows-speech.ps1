# Speech on Windows (G8): a side server on 22498 runs the opencoti engine THIS
# TREE pins, and each clip is transcribed back by Whisper.
#
# The engine is never named here. Stage it on solidPC from the pin and copy it
# over, then run this with -File:
#   scripts/gates/stage-engine.sh win-x86_64 /srv/ml/gates/engine-win
#   scp -r /srv/ml/gates/engine-win 'eleven2go:C:/Users/ManniX/xollama-gate/engine'
#   ssh eleven2go "powershell -NoProfile -ExecutionPolicy Bypass -File C:\Users\ManniX\xollama-gate\windows-speech.ps1"
# The directory must hold exactly one engine, and its sha256 must be the one
# engine-manifest.txt (written by the stager from the pin) names.
# -Xollama is the server binary (default: the installed xollama.exe); -Models a
# store of the speech models that this server may write (since upstream v0.40.0
# it creates manifests-v2 in the store it serves).
param(
  [string]$EngineDir = "$env:USERPROFILE\xollama-gate\engine",
  [string]$Xollama = "$env:LOCALAPPDATA\Programs\xOllama\xollama.exe",
  [string]$Models = "$env:USERPROFILE\xollama-b120\models"
)
$ErrorActionPreference = 'Stop'
$d = Split-Path $EngineDir; $u = 'http://127.0.0.1:22498'; $log = "$d\serve-speech.log"

$rows = @(Get-Content "$EngineDir\engine-manifest.txt" | Where-Object { $_ -match '^engine \S+ bin ' })
if ($rows.Count -ne 1) { throw "engine-manifest.txt in $EngineDir must name one engine" }
$m = $rows[0] -split ' '
$eng = Join-Path $EngineDir $m[3]
$found = @(Get-ChildItem $EngineDir -Filter 'opencoti*.exe')
if ($found.Count -ne 1 -or $found[0].FullName -ne $eng) { throw "expected only $($m[3]) in $EngineDir, found: $($found.Name -join ', ')" }
$sum = (Get-FileHash $eng -Algorithm SHA256).Hash.ToLower()
if ($sum -ne $m[4]) { throw "$($m[3]) is not the pinned bytes: $sum, pin $($m[4])" }
"engine: $($m[3]) version $($m[1]), sha256 matches the pin"
"server: $(& $Xollama --version 2>&1 | Select-Object -Last 1)"

if (Get-NetTCPConnection -State Listen -LocalPort 22498 -ErrorAction SilentlyContinue) { throw "22498 is busy" }
Remove-Item Env:XOLLAMA_ENGINE_ARGS -ErrorAction SilentlyContinue
$env:XOLLAMA_ENGINE = 'opencoti'; $env:XOLLAMA_ENGINE_PATH = $eng; $env:OLLAMA_MODELS = $Models; $env:XOLLAMA_HOST = '127.0.0.1:22498'
New-Item -ItemType Directory -Force "$d\out" | Out-Null; Remove-Item $log, "$log.err" -ErrorAction SilentlyContinue
$p = Start-Process -FilePath $Xollama -ArgumentList 'serve' -WindowStyle Hidden -RedirectStandardOutput $log -RedirectStandardError "$log.err" -PassThru
try {
  $ok = $false; for ($n = 0; $n -lt 60 -and -not $ok; $n++) { Start-Sleep 1; try { Invoke-RestMethod "$u/api/version" -TimeoutSec 2 | Out-Null; $ok = $true } catch {} }
  if (-not $ok) { throw "the side server did not answer on 22498" }
  "models: $(((Invoke-RestMethod "$u/api/tags").models | ForEach-Object name) -join ', ')"
  $ErrorActionPreference = 'Continue'
  foreach ($s in 'mannix/kokoro:82m','mannix/kittentts:mini-0.8','mannix/supertonic:3','mannix/outetts:0.3') {
    $f = "$d\out\$($s -replace '[/:]','_').mp3"; $b = @{ model = $s; input = 'The first ship came in at dawn.' } | ConvertTo-Json
    try { Invoke-WebRequest "$u/v1/audio/speech" -Method Post -ContentType 'application/json' -Body $b -OutFile $f -TimeoutSec 600 -UseBasicParsing; $t = (& curl.exe -s -m 600 "$u/v1/audio/transcriptions" -F model=mannix/whisper:large-v3-turbo -F "file=@$f"); "$s -> $((Get-Item $f).Length) bytes -> $t" } catch { "$s FAILED: $($_.ErrorDetails.Message) $($_.Exception.Message)" }
  }
  # The engine that answered must be the pinned one: its own build line.
  Get-Content "$log.err" | Select-String -Pattern 'opencoti build','espeak: ','audiocpp: ','codec: ' | Select-Object -First 12 | ForEach-Object { "  log: $($_.Line.Substring(0, [math]::Min(200, $_.Line.Length)))" }
  "engine build lines naming $($m[1]): $(@(Select-String -Path "$log.err" -Pattern "build $($m[1])").Count)"
} finally {
  Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue; Start-Sleep 2
  Remove-Item Env:XOLLAMA_HOST, Env:OLLAMA_MODELS, Env:XOLLAMA_ENGINE_PATH, Env:XOLLAMA_ENGINE -ErrorAction SilentlyContinue
}
Get-NetTCPConnection -State Listen -LocalPort 22434,11434,22498 -ErrorAction SilentlyContinue | ForEach-Object { "port $($_.LocalPort) pid $($_.OwningProcess)" }
"ollama 11434: $((Invoke-RestMethod http://127.0.0.1:11434/api/version -TimeoutSec 5).version); nvidia: $(nvidia-smi --query-gpu=memory.used --format=csv,noheader)"
