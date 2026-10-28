# Restarts the installed xOllama app and lists every console window that becomes
# VISIBLE while it starts (the no-console hook, docs/features/windows-installer.md).
# It must run in the logged-in session, where the app's windows are; an ssh
# session cannot see them. Through windows-gate-run.ps1:
#   ssh <host> "powershell -NoProfile -ExecutionPolicy Bypass -File C:\Users\ManniX\xollama-gate\windows-gate-run.ps1 -Script C:\Users\ManniX\xollama-gate\windows-console-probe.ps1 -Out C:\Users\ManniX\xollama-gate\probe.out -Minutes 5"
# A process trace is not the test: with CREATE_NO_WINDOW PowerShell still gets
# a conhost.exe, only without a window.
param([int]$Seconds = 60)
Add-Type @'
using System; using System.Collections.Generic; using System.Runtime.InteropServices; using System.Text;
public static class Wins {
  delegate bool EnumProc(IntPtr h, IntPtr l);
  [DllImport("user32.dll")] static extern bool EnumWindows(EnumProc f, IntPtr l);
  [DllImport("user32.dll")] static extern bool IsWindowVisible(IntPtr h);
  [DllImport("user32.dll", CharSet = CharSet.Unicode)] static extern int GetClassName(IntPtr h, StringBuilder s, int n);
  [DllImport("user32.dll", CharSet = CharSet.Unicode)] static extern int GetWindowText(IntPtr h, StringBuilder s, int n);
  [DllImport("user32.dll")] static extern uint GetWindowThreadProcessId(IntPtr h, out uint pid);
  public static List<string> Consoles() {
    var r = new List<string>();
    EnumWindows((h, l) => {
      if (!IsWindowVisible(h)) return true;
      var c = new StringBuilder(256); GetClassName(h, c, 256);
      var k = c.ToString();
      if (k == "ConsoleWindowClass" || k == "CASCADIA_HOSTING_WINDOW_CLASS" || k == "PseudoConsoleWindow") {
        var t = new StringBuilder(512); GetWindowText(h, t, 512); uint pid; GetWindowThreadProcessId(h, out pid);
        r.Add(h.ToInt64() + "|" + k + "|" + pid + "|" + t.ToString());
      }
      return true;
    }, IntPtr.Zero);
    return r;
  }
}
'@
$app = "$env:LOCALAPPDATA\Programs\xOllama\xOllama app.exe"
"version: $(& "$env:LOCALAPPDATA\Programs\xOllama\xollama.exe" --version 2>&1 | Select-Object -Last 1)"
$before = @{}; foreach ($w in [Wins]::Consoles()) { $before[$w.Split('|')[0]] = $true }
"console windows already open (ignored): $($before.Count)"
Get-Process 'xOllama app', 'xollama' -ErrorAction SilentlyContinue | ForEach-Object { Stop-Process -Id $_.Id -Force }
Start-Sleep 3
$t0 = Get-Date; Start-Process $app
$seen = [ordered]@{}
while (((Get-Date) - $t0).TotalSeconds -lt $Seconds) {
  foreach ($w in [Wins]::Consoles()) {
    $f = $w.Split('|'); if ($before.ContainsKey($f[0]) -or $seen.Contains($f[0])) { continue }
    $p = Get-Process -Id ([int]$f[2]) -ErrorAction SilentlyContinue
    $seen[$f[0]] = "{0,5:N1}s {1} pid {2} ({3}) title '{4}'" -f ((Get-Date) - $t0).TotalSeconds, $f[1], $f[2], $p.ProcessName, $f[3]
  }
  Start-Sleep -Milliseconds 50
}
"new visible console windows while xOllama started: $($seen.Count)"
$seen.Values | ForEach-Object { "  $_" }
"app running: $((Get-Process 'xOllama app' -ErrorAction SilentlyContinue).Id); server: $((Get-Process 'xollama' -ErrorAction SilentlyContinue).Id)"
