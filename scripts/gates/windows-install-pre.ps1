$i = "$env:LOCALAPPDATA\Programs\xOllama"
"before: version $(& "$i\xollama.exe" --version 2>&1 | Select-Object -Last 1)"
"before: PAYLOAD_ID $(Get-Content "$i\lib\ollama\PAYLOAD_ID" -ErrorAction SilentlyContinue)"
Get-NetTCPConnection -State Listen -LocalPort 22434,11434 -ErrorAction SilentlyContinue | ForEach-Object { "port $($_.LocalPort) pid $($_.OwningProcess) $((Get-Process -Id $_.OwningProcess).ProcessName) started $((Get-Process -Id $_.OwningProcess).StartTime)" }
"ollama 11434: $((Invoke-RestMethod http://127.0.0.1:11434/api/version -TimeoutSec 5).version)"
"sessions: $((query user 2>&1 | Out-String).Trim())"
"installer sha256 $((Get-FileHash "$env:USERPROFILE\Downloads\xOllamaSetup.exe" -Algorithm SHA256).Hash.ToLower())"
"AMD drivers: $((Get-CimInstance Win32_VideoController | ForEach-Object { "$($_.Name) $($_.DriverVersion)" }) -join "; ")"
"loaded on 22434: $(((Invoke-RestMethod http://127.0.0.1:22434/api/ps -TimeoutSec 10).models | ForEach-Object name) -join ", ")"
