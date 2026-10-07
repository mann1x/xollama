# bug-3954 at the engine: one model over the RTX 3090 and the RX 9070 XT, both
# on Vulkan, layers split 1:1 (-ts 1,1), the way opencoti reproduced it. The
# xollama path cannot place one model on both cards here: Windows Vulkan
# devices come without a PCI ID, so the 3090 cannot be moved off CUDA, and the
# scheduler never splits across backends. A raw prompt (no chat template, so
# the two requests share no prefix) is asked on a fresh engine and again after
# another request; per-token logprobs must agree. Run once per engine.
#   powershell -File windows-twogpu-engine.ps1 -EngineDir <dir> -Blob <gguf>
param(
  [Parameter(Mandatory)][string]$EngineDir,
  [Parameter(Mandatory)][string]$Blob,
  [string]$Work = "$env:USERPROFILE\xollama-gate\twogpu"
)
$ErrorActionPreference = 'Stop'
$port = 22499; $u = "http://127.0.0.1:$port"
$eng = @(Get-ChildItem $EngineDir -Filter 'opencoti*.exe')
if ($eng.Count -ne 1) { throw "expected one engine in $EngineDir" }
$eng = $eng[0].FullName; $tag = [IO.Path]::GetFileNameWithoutExtension($eng)
"engine: $tag sha256 $((Get-FileHash $eng -Algorithm SHA256).Hash.ToLower())"
if (Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue) { throw "$port is busy" }

# Resolve the two cards by name from the engine's own list, never by a fixed index.
$ErrorActionPreference = 'Continue'
$list = & $eng --server --list-devices -m /nonexistent.gguf --offline --verbose --gpu vulkan 2>&1 | Out-String
$ErrorActionPreference = 'Stop'
$dev = foreach ($want in 'RTX 3090', 'RX 9070 XT') {
  $m = [regex]::Match($list, "(Vulkan\d+): [^\r\n]*$([regex]::Escape($want))")
  if (-not $m.Success) { throw "no Vulkan device named $want in:`n$list" }
  $m.Groups[1].Value
}
"devices: $($dev -join ',') (3090, 9070 XT)"

$long = ((1..60) | ForEach-Object { "Line $($_): the harbour log records ship $($_ * 7) arriving with cargo of grain, timber and salt." }) -join "`n"
$reqA = @{ prompt = "$long`n`nA five-sentence summary of the harbour log:"; n_predict = 256; temperature = 0; seed = 11; n_probs = 1; cache_prompt = $true } | ConvertTo-Json
$reqB = @{ prompt = 'Once upon a time a lighthouse keeper watched a storm roll in from the west.'; n_predict = 300; temperature = 0.8; seed = 5; cache_prompt = $true } | ConvertTo-Json

function Boot($phase) {
  $log = "$Work\engine-$tag-$phase.log"
  $cmdline = "--server --gpu vulkan --model `"$Blob`" --device $($dev -join ',') -ts 1,1 -ngl 99 -c 8192 -np 1 --port $port --host 127.0.0.1"
  $p = Start-Process -FilePath $eng -ArgumentList $cmdline -WindowStyle Hidden -RedirectStandardOutput "$log.out" -RedirectStandardError $log -PassThru
  $ok = $false; for ($n = 0; $n -lt 180 -and -not $ok; $n++) { Start-Sleep 1; try { $h = Invoke-RestMethod "$u/health" -TimeoutSec 2; $ok = $h.status -eq 'ok' } catch {} }
  if (-not $ok) { Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue; throw "engine did not become healthy; see $log" }
  $p
}
function Halt($p) { Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue; for ($n = 0; $n -lt 30 -and (Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue); $n++) { Start-Sleep 1 }; Start-Sleep 3 }
function Ask($body) { Invoke-RestMethod "$u/completion" -Method Post -ContentType 'application/json' -Body $body -TimeoutSec 900 }
function Probs($r) { @($r.completion_probabilities | ForEach-Object { if ($null -ne $_.logprob) { [pscustomobject]@{ t = $_.token; l = [double]$_.logprob } } else { [pscustomobject]@{ t = $_.content; l = [math]::Log([double]$_.probs[0].prob) } } }) }

try {
  $p = Boot 'fresh'; $fresh = Ask $reqA; Halt $p
  $p = Boot 'after'; $null = Ask $reqB; $after = Ask $reqA; Halt $p
} finally {
  Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue | ForEach-Object { Stop-Process -Id $_.OwningProcess -Force -ErrorAction SilentlyContinue }
}
Get-Content "$Work\engine-$tag-fresh.log" | Select-String -Pattern 'offloaded', 'position window', 'using device', 'Vulkan\d+ model buffer' | Select-Object -First 6 |
  ForEach-Object { "  log: $($_.Line.Substring(0, [math]::Min(220, $_.Line.Length)))" }
$a = Probs $fresh; $b = Probs $after
$n = [math]::Min($a.Count, $b.Count); $max = 0.0; $first = -1
for ($i = 0; $i -lt $n; $i++) {
  $d = [math]::Abs($a[$i].l - $b[$i].l); if ($d -gt $max) { $max = $d }
  if ($first -lt 0 -and $a[$i].t -ne $b[$i].t) { $first = $i }
}
"tokens: fresh $($a.Count), after $($b.Count); first differing token: $first; max |dlogprob| over ${n}: {0:F6}" -f $max
if ($n -eq 0) { "RESULT: NO-PROBS" } elseif ($max -eq 0 -and $first -lt 0) { "RESULT: IDENTICAL" } else { "RESULT: DIFFERS" }
