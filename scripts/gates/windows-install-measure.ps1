$x = "$env:LOCALAPPDATA\Programs\xOllama\xollama.exe"; $u = 'http://127.0.0.1:22434'; $log = "$env:LOCALAPPDATA\xOllama\server.log"
$pins = [ordered]@{ 'rc1/qwen3-cuda' = @('CUDA','0000:11:00.0'); 'rc1/qwen3-vk9070' = @('Vulkan','name:AMD Radeon RX 9070 XT'); 'rc1/qwen3-vkigpu' = @('Vulkan','integrated') }
foreach ($t in $pins.Keys) { & $x cp qwen3:8b $t 2>&1 | Out-Null; & $x tweak model $t "--device-backend=$($pins[$t][0])" "--devices=$($pins[$t][1])" "--engine=opencoti" 2>&1 | Select-Object -Last 1 }
$prompt = 'Write a detailed 1500-word essay on the history of the steam engine, from Newcomen to the end of the age of steam. Do not stop early.'
foreach ($t in $pins.Keys) {
  "=== $t ($($pins[$t] -join ' '))"
  $mark = (Get-Content $log).Count
  for ($r = 0; $r -lt 5; $r++) {
    $b = @{ model = $t; prompt = $prompt; stream = $false; think = $false; options = @{ num_predict = 512; seed = 7 + $r } } | ConvertTo-Json -Depth 5
    try { $g = Invoke-RestMethod "$u/api/generate" -Method Post -ContentType 'application/json' -Body $b -TimeoutSec 900; "  run $r $(if ($r -eq 0) {'cold'} else {'warm'}): {0} tokens {1:N1} tok/s, prompt {2} tokens {3:N1} tok/s, load {4:N1}s" -f $g.eval_count, ($g.eval_count / ($g.eval_duration / 1e9)), $g.prompt_eval_count, ($g.prompt_eval_count / ([math]::Max($g.prompt_eval_duration,1) / 1e9)), ($g.load_duration / 1e9) } catch { "  run $r FAILED: $($_.ErrorDetails.Message) $($_.Exception.Message)"; break }
  }
  (Invoke-RestMethod "$u/api/ps").models | ForEach-Object { "  ps: $($_.name) size {0:N1} GB vram {1:N1} GB ctx $($_.context_length)" -f ($_.size/1GB), ($_.size_vram/1GB) }
  "  nvidia: $(nvidia-smi --query-gpu=memory.used,utilization.gpu --format=csv,noheader)"
  Get-Content $log | Select-Object -Skip $mark | Select-String -Pattern 'model placement','starting engine','engine=','offloaded','ggml-cuda','ggml-vulkan','using device','level=WARN','level=ERROR' | Select-Object -First 14 | ForEach-Object { "  log: $($_.Line.Substring(0, [math]::Min(330, $_.Line.Length)))" }
  Invoke-RestMethod "$u/api/generate" -Method Post -ContentType 'application/json' -Body (@{ model = $t; keep_alive = 0 } | ConvertTo-Json) -TimeoutSec 120 | Out-Null
  Start-Sleep 3
}
"ollama 11434: $((Invoke-RestMethod http://127.0.0.1:11434/api/version -TimeoutSec 5).version)"
foreach ($t in $pins.Keys) { & $x rm $t 2>&1 | Out-Null }
"display: $((Get-PnpDevice -Class Display -PresentOnly | ForEach-Object { "$($_.FriendlyName)=$($_.Status)" }) -join '; ')"
