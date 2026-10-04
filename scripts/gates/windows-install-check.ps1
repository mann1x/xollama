$i = "$env:LOCALAPPDATA\Programs\xOllama"
"version: $(& "$i\xollama.exe" --version 2>&1 | Select-Object -Last 1)"
"PAYLOAD_ID: $(Get-Content "$i\lib\ollama\PAYLOAD_ID")"
Get-NetTCPConnection -State Listen -LocalPort 22434,11434 -ErrorAction SilentlyContinue | ForEach-Object { "port $($_.LocalPort) addr $($_.LocalAddress) pid $($_.OwningProcess) $((Get-Process -Id $_.OwningProcess).ProcessName) started $((Get-Process -Id $_.OwningProcess).StartTime)" }
"identity: $((Invoke-RestMethod http://127.0.0.1:22434/api/xollama -TimeoutSec 10) | ConvertTo-Json -Compress)"
"devices: $((Invoke-RestMethod http://127.0.0.1:22434/api/xollama/devices -TimeoutSec 60) | ConvertTo-Json -Compress -Depth 6)"
"ollama 11434: $((Invoke-RestMethod http://127.0.0.1:11434/api/version -TimeoutSec 5).version)"
"engines dir:"
Get-ChildItem "$i\lib\ollama\engines" | ForEach-Object { "  {0,12} {1}" -f $_.Length, $_.Name }
"models:"
(Invoke-RestMethod http://127.0.0.1:22434/api/tags -TimeoutSec 20).models | ForEach-Object { "  {0,-44} {1,6:N1} GB {2} {3}" -f $_.name, ($_.size/1GB), $_.details.parameter_size, $_.details.quantization_level }
"nvidia: $(nvidia-smi --query-gpu=name,memory.used,memory.total,utilization.gpu --format=csv,noheader)"
