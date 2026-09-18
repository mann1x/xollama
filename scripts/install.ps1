<#
.SYNOPSIS
    Install, upgrade, or uninstall xOllama on Windows.

.DESCRIPTION
    Downloads and installs xOllama.

    Quick install:

        irm https://raw.githubusercontent.com/mann1x/xollama/main/scripts/install.ps1 | iex

    Specific version:

        $env:XOLLAMA_VERSION="0.34.2"; irm https://raw.githubusercontent.com/mann1x/xollama/main/scripts/install.ps1 | iex

    Custom install directory:

        $env:XOLLAMA_INSTALL_DIR="D:\xOllama"; irm https://raw.githubusercontent.com/mann1x/xollama/main/scripts/install.ps1 | iex

    Uninstall:

        $env:XOLLAMA_UNINSTALL=1; irm https://raw.githubusercontent.com/mann1x/xollama/main/scripts/install.ps1 | iex

    Environment variables:

        OLLAMA_VERSION       Target version (default: latest stable)
        OLLAMA_INSTALL_DIR   Custom install directory
        XOLLAMA_UNINSTALL    Set to 1 to uninstall xOllama
        OLLAMA_DEBUG         Enable verbose output

.EXAMPLE
    irm https://raw.githubusercontent.com/mann1x/xollama/main/scripts/install.ps1 | iex

.EXAMPLE
    $env:XOLLAMA_VERSION = "0.34.2"; irm https://raw.githubusercontent.com/mann1x/xollama/main/scripts/install.ps1 | iex

.LINK
    https://github.com/mann1x/xollama
#>

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

# --------------------------------------------------------------------------
# Configuration from environment variables
# --------------------------------------------------------------------------

$Version      = if ($env:XOLLAMA_VERSION) { $env:XOLLAMA_VERSION } elseif ($env:OLLAMA_VERSION) { $env:OLLAMA_VERSION } else { "" }
$InstallDir   = if ($env:XOLLAMA_INSTALL_DIR) { $env:XOLLAMA_INSTALL_DIR } elseif ($env:OLLAMA_INSTALL_DIR) { $env:OLLAMA_INSTALL_DIR } else { "" }
$Uninstall    = ($env:XOLLAMA_UNINSTALL -eq "1") -or ($env:OLLAMA_UNINSTALL -eq "1")
$DebugInstall = [bool]($env:XOLLAMA_DEBUG) -or [bool]($env:OLLAMA_DEBUG)

# --------------------------------------------------------------------------
# Constants
# --------------------------------------------------------------------------

# XOLLAMA_DOWNLOAD_URL for developer testing only. The default is this fork's
# GitHub releases: ollama.com/download serves UPSTREAM ollama, so pointing here
# is what stops the script installing stock ollama under our name.
$ReleasesURL = "https://github.com/mann1x/xollama/releases"
$DownloadBaseURL = if ($env:XOLLAMA_DOWNLOAD_URL) { $env:XOLLAMA_DOWNLOAD_URL.TrimEnd('/') }
                   elseif ($env:OLLAMA_DOWNLOAD_URL) { $env:OLLAMA_DOWNLOAD_URL.TrimEnd('/') }
                   elseif ($Version) { "$ReleasesURL/download/v$($Version.TrimStart('v'))" }
                   else { "$ReleasesURL/latest/download" }

# MUST be xollama's own AppId, not upstream's. This GUID is what the script
# looks up to find an existing install and to uninstall it -- pointed at
# upstream's key, `XOLLAMA_UNINSTALL=1` would uninstall the user's real Ollama.
# It is also what stops `winget upgrade` replacing xollama with stock ollama.
$InnoSetupUninstallGuid = "{F6F806B4-09B2-43BA-8413-B1D52561CA60}_is1"

# Authenticode signer to require, e.g. "O=Example Ltd.". Empty means this fork
# publishes unsigned builds, and integrity is established by the SHA256 in the
# release's sha256sum.txt instead -- see Test-Installer.
$ExpectedSigner = if ($env:XOLLAMA_EXPECTED_SIGNER) { $env:XOLLAMA_EXPECTED_SIGNER } else { "" }

# --------------------------------------------------------------------------
# Helpers
# --------------------------------------------------------------------------

function Write-Status {
    param([string]$Message)
    if ($DebugInstall) { Write-Host $Message }
}

function Write-Step {
    param([string]$Message)
    if ($DebugInstall) { Write-Host ">>> $Message" -ForegroundColor Cyan }
}

function Test-Installer {
    param([string]$FilePath, [string]$FileName)

    # Upstream required an Authenticode signature by "O=Ollama Inc." and threw
    # otherwise. This fork is not signed by Ollama Inc. and must not pretend to
    # be, so that check cannot simply be retargeted -- and dropping it outright
    # would install whatever the network handed us.
    #
    # If a signer is pinned (XOLLAMA_EXPECTED_SIGNER), require it. Otherwise
    # verify the download against the SHA256 published with the release, which
    # is the integrity story an unsigned GitHub release actually has. Either
    # way a failure is fatal at the call site.
    if ($ExpectedSigner) {
        $sig = Get-AuthenticodeSignature -FilePath $FilePath
        if ($sig.Status -ne "Valid") {
            Write-Status "  Signature status: $($sig.Status)"
            return $false
        }
        $subject = $sig.SignerCertificate.Subject
        if ($subject -notmatch [regex]::Escape($ExpectedSigner)) {
            Write-Status "  Unexpected signer: $subject (wanted $ExpectedSigner)"
            return $false
        }
        Write-Status "  Signature valid: $subject"
        return $true
    }

    $sumsUrl = "$DownloadBaseURL/sha256sum.txt"
    try {
        $sums = (Invoke-WebRequest -UseBasicParsing -Uri $sumsUrl).Content
    } catch {
        Write-Status "  Could not fetch $sumsUrl : $_"
        return $false
    }

    $actual = (Get-FileHash -Path $FilePath -Algorithm SHA256).Hash.ToLower()
    $expected = $null
    foreach ($line in ($sums -split "`n")) {
        $parts = $line.Trim() -split '\s+', 2
        if ($parts.Count -eq 2 -and (Split-Path -Leaf $parts[1].Trim()) -eq $FileName) {
            $expected = $parts[0].Trim().ToLower()
            break
        }
    }
    if (-not $expected) {
        Write-Status "  $FileName is not listed in sha256sum.txt"
        return $false
    }
    if ($actual -ne $expected) {
        Write-Status "  SHA256 mismatch: got $actual, expected $expected"
        return $false
    }
    Write-Status "  SHA256 verified: $actual"
    return $true
}

function Find-InnoSetupInstall {
    # Check both HKCU (per-user) and HKLM (per-machine) locations
    $possibleKeys = @(
        "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\$InnoSetupUninstallGuid",
        "HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\$InnoSetupUninstallGuid",
        "HKLM:\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\$InnoSetupUninstallGuid"
    )

    foreach ($key in $possibleKeys) {
        if (Test-Path $key) {
            Write-Status "  Found install at: $key"
            return $key
        }
    }
    return $null
}

function Update-SessionPath {
    # Update PATH in current session so 'ollama' works immediately
    if ($InstallDir) {
        $ollamaDir = $InstallDir
    } else {
        $ollamaDir = Join-Path $env:LOCALAPPDATA "Programs\xOllama"
    }

    # Add to PATH if not already present
    if (Test-Path $ollamaDir) {
        $currentPath = $env:PATH -split ';'
        if ($ollamaDir -notin $currentPath) {
            $env:PATH = "$ollamaDir;$env:PATH"
            Write-Status "  Added $ollamaDir to session PATH"
        }
    }
}

function Invoke-Download {
    param(
        [string]$Url,
        [string]$OutFile
    )

    Write-Status "  Downloading: $Url"
    try {
        $request = [System.Net.HttpWebRequest]::Create($Url)
        $request.AllowAutoRedirect = $true
        $response = $request.GetResponse()
        $totalBytes = $response.ContentLength
        $stream = $response.GetResponseStream()
        $fileStream = [System.IO.FileStream]::new($OutFile, [System.IO.FileMode]::Create)
        $buffer = [byte[]]::new(65536)
        $totalRead = 0
        $lastUpdate = [DateTime]::MinValue
        $barWidth = 40

        try {
            while (($read = $stream.Read($buffer, 0, $buffer.Length)) -gt 0) {
                $fileStream.Write($buffer, 0, $read)
                $totalRead += $read

                $now = [DateTime]::UtcNow
                if (($now - $lastUpdate).TotalMilliseconds -ge 250) {
                    if ($totalBytes -gt 0) {
                        $pct = [math]::Min(100.0, ($totalRead / $totalBytes) * 100)
                        $filled = [math]::Floor($barWidth * $pct / 100)
                        $empty = $barWidth - $filled
                        $bar = ('#' * $filled) + (' ' * $empty)
                        $pctFmt = $pct.ToString("0.0")
                        Write-Host -NoNewline "`r$bar ${pctFmt}%"
                    } else {
                        $sizeMB = [math]::Round($totalRead / 1MB, 1)
                        Write-Host -NoNewline "`r${sizeMB} MB downloaded..."
                    }
                    $lastUpdate = $now
                }
            }

            # Final progress update
            if ($totalBytes -gt 0) {
                $bar = '#' * $barWidth
                Write-Host "`r$bar 100.0%"
            } else {
                $sizeMB = [math]::Round($totalRead / 1MB, 1)
                Write-Host "`r${sizeMB} MB downloaded.          "
            }
        } finally {
            $fileStream.Close()
            $stream.Close()
            $response.Close()
        }
    } catch {
        if ($_.Exception -is [System.Net.WebException]) {
            $webEx = [System.Net.WebException]$_.Exception
            if ($webEx.Response -and ([System.Net.HttpWebResponse]$webEx.Response).StatusCode -eq [System.Net.HttpStatusCode]::NotFound) {
                throw "Download failed: not found at $Url"
            }
        }
        if ($_.Exception.InnerException -is [System.Net.WebException]) {
            $webEx = [System.Net.WebException]$_.Exception.InnerException
            if ($webEx.Response -and ([System.Net.HttpWebResponse]$webEx.Response).StatusCode -eq [System.Net.HttpStatusCode]::NotFound) {
                throw "Download failed: not found at $Url"
            }
        }
        throw "Download failed for ${Url}: $($_.Exception.Message)"
    }
}

# --------------------------------------------------------------------------
# Uninstall
# --------------------------------------------------------------------------

function Invoke-Uninstall {
    Write-Step "Uninstalling xOllama"

    $regKey = Find-InnoSetupInstall
    if (-not $regKey) {
        Write-Host ">>> xOllama is not installed."
        return
    }

    $uninstallString = (Get-ItemProperty -Path $regKey).UninstallString
    if (-not $uninstallString) {
        Write-Warning "No uninstall string found in registry"
        return
    }

    # Strip quotes if present
    $uninstallExe = $uninstallString -replace '"', ''
    Write-Status "  Uninstaller: $uninstallExe"

    if (-not (Test-Path $uninstallExe)) {
        Write-Warning "Uninstaller not found at: $uninstallExe"
        return
    }

    Write-Host ">>> Launching uninstaller..."
    # Run with GUI so user can choose whether to keep models
    Start-Process -FilePath $uninstallExe -Wait

    # Verify removal
    if (Find-InnoSetupInstall) {
        Write-Warning "Uninstall may not have completed"
    } else {
        Write-Host ">>> xOllama has been uninstalled."
    }
}

# --------------------------------------------------------------------------
# Install
# --------------------------------------------------------------------------

function Invoke-Install {
    # The version, when pinned, is already encoded in $DownloadBaseURL as a
    # GitHub release path, so there is nothing left to branch on here.
    $installerUrl = "$DownloadBaseURL/xOllamaSetup.exe"

    # Download installer
    Write-Step "Downloading xOllama"
    if (-not $DebugInstall) {
        Write-Host ">>> Downloading xOllama for Windows..."
    }

    $tempInstaller = Join-Path $env:TEMP "xOllamaSetup.exe"
    Invoke-Download -Url $installerUrl -OutFile $tempInstaller

    # Verify signature
    Write-Step "Verifying signature"
    if (-not (Test-Installer -FilePath $tempInstaller -FileName "xOllamaSetup.exe")) {
        Remove-Item $tempInstaller -Force -ErrorAction SilentlyContinue
        throw "Installer verification failed"
    }

    # Build installer arguments
    $installerArgs = "/VERYSILENT /NORESTART /SUPPRESSMSGBOXES"
    if ($InstallDir) {
        $installerArgs += " /DIR=`"$InstallDir`""
    }
    Write-Status "  Installer args: $installerArgs"

    # Run installer
    Write-Step "Installing xOllama"
    if (-not $DebugInstall) {
        Write-Host ">>> Installing Ollama..."
    }

    # Create upgrade marker so the app starts hidden
    # The app checks for this file on startup and removes it after
    $markerDir = Join-Path $env:LOCALAPPDATA "Ollama"
    $markerFile = Join-Path $markerDir "upgraded"
    if (-not (Test-Path $markerDir)) {
        New-Item -ItemType Directory -Path $markerDir -Force | Out-Null
    }
    New-Item -ItemType File -Path $markerFile -Force | Out-Null
    Write-Status "  Created upgrade marker: $markerFile"

    # Start installer and wait for just the installer process (not children)
    # Using -Wait would wait for Ollama to exit too, which we don't want
    $proc = Start-Process -FilePath $tempInstaller `
        -ArgumentList $installerArgs `
        -PassThru
    $proc.WaitForExit()

    if ($proc.ExitCode -ne 0) {
        Remove-Item $tempInstaller -Force -ErrorAction SilentlyContinue
        throw "Installation failed with exit code $($proc.ExitCode)"
    }

    # Cleanup
    Remove-Item $tempInstaller -Force -ErrorAction SilentlyContinue

    # Update PATH in current session so 'ollama' works immediately
    Write-Step "Updating session PATH"
    Update-SessionPath

    Write-Host ">>> Install complete. Run 'ollama' from the command line."
}

# --------------------------------------------------------------------------
# Main
# --------------------------------------------------------------------------

if ($Uninstall) {
    Invoke-Uninstall
} else {
    Invoke-Install
}
