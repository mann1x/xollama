$exe = "$env:USERPROFILE\Downloads\xOllamaSetup.exe"; $log = "$env:USERPROFILE\Downloads\xOllamaSetup-gate.log"
Remove-Item $log -ErrorAction SilentlyContinue
$a = New-ScheduledTaskAction -Execute $exe -Argument "/SILENT /SUPPRESSMSGBOXES /NORESTART /LOG=`"$log`""
$p = New-ScheduledTaskPrincipal -UserId (whoami) -LogonType Interactive
Register-ScheduledTask -TaskName 'xollama-gate-install' -Action $a -Principal $p -Force | Out-Null
$sw = [Diagnostics.Stopwatch]::StartNew()
Start-ScheduledTask -TaskName 'xollama-gate-install'
Start-Sleep 3
while ((Get-ScheduledTask -TaskName 'xollama-gate-install').State -eq 'Running' -and $sw.Elapsed.TotalSeconds -lt 900) { Start-Sleep 2 }
$r = Get-ScheduledTaskInfo -TaskName 'xollama-gate-install'
"installer: state $((Get-ScheduledTask -TaskName 'xollama-gate-install').State) result $($r.LastTaskResult) after {0:N0}s" -f $sw.Elapsed.TotalSeconds
Unregister-ScheduledTask -TaskName 'xollama-gate-install' -Confirm:$false
if (Test-Path $log) { Get-Content $log -Tail 6 }
