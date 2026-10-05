param([string]$Exe = "$env:USERPROFILE\Downloads\xOllamaUpdate.exe")
$exe = $Exe; $log = "$env:USERPROFILE\Downloads\xOllamaUpdate-gate.log"
Remove-Item $log -ErrorAction SilentlyContinue
$a = New-ScheduledTaskAction -Execute $exe -Argument "/SILENT /SUPPRESSMSGBOXES /NORESTART /LOG=`"$log`""
$p = New-ScheduledTaskPrincipal -UserId (whoami) -LogonType Interactive
Register-ScheduledTask -TaskName 'xollama-gate-update' -Action $a -Principal $p -Force | Out-Null
$sw = [Diagnostics.Stopwatch]::StartNew()
Start-ScheduledTask -TaskName 'xollama-gate-update'
Start-Sleep 3
while ((Get-ScheduledTask -TaskName 'xollama-gate-update').State -eq 'Running' -and $sw.Elapsed.TotalSeconds -lt 900) { Start-Sleep 2 }
$r = Get-ScheduledTaskInfo -TaskName 'xollama-gate-update'
"installer: state $((Get-ScheduledTask -TaskName 'xollama-gate-update').State) result $($r.LastTaskResult) after {0:N0}s" -f $sw.Elapsed.TotalSeconds
Unregister-ScheduledTask -TaskName 'xollama-gate-update' -Confirm:$false
if (Test-Path $log) { Get-Content $log -Tail 6 }
