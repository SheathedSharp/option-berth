#Requires -Version 5.1
<#
.SYNOPSIS
Install the released option-berth CLI on Windows without a Go toolchain.

.DESCRIPTION
Resolves the latest engine release, downloads the Windows archive for this
machine, verifies its SHA256 sidecar, and installs oberth.exe + jev-attention.exe
into %LOCALAPPDATA%\option-berth\bin. It starts no services and writes no Git data.

When the official GitHub host is unreachable, downloads fall back to a short list
of GitHub proxies. The checksum is still verified and, when it comes from a
mirror, corroborated by a second source. Set BERTH_INSTALL_MIRRORS='' to use only
the official host.

.EXAMPLE
irm https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.ps1 | iex

.EXAMPLE
.\install.ps1 -Version v1.0.0 -Dir C:\tools\berth -NoModifyPath
#>
[CmdletBinding()]
param(
    [string]$Version = $env:BERTH_INSTALL_VERSION,
    [string]$Dir = $env:BERTH_INSTALL_DIR,
    [switch]$NoModifyPath
)

$ErrorActionPreference = 'Stop'

function Install-Oberth {
    $repo = if ($env:BERTH_INSTALL_REPO) { $env:BERTH_INSTALL_REPO } else { 'SheathedSharp/option-berth' }
    $baseUrl = if ($env:BERTH_INSTALL_BASE_URL) { $env:BERTH_INSTALL_BASE_URL } else { "https://github.com/$repo/releases/download" }
    $latestUrl = if ($env:BERTH_INSTALL_LATEST_URL) { $env:BERTH_INSTALL_LATEST_URL } else { "https://api.github.com/repos/$repo/releases/latest" }
    $mirrorSetting = if ($null -ne $env:BERTH_INSTALL_MIRRORS) {
        $env:BERTH_INSTALL_MIRRORS
    }
    else {
        'https://ghfast.top https://gh-proxy.com'
    }
    $mirrors = @($mirrorSetting -split '\s+' | Where-Object { $_ } | ForEach-Object { $_.TrimEnd('/') })
    $assetBases = @($baseUrl) + @($mirrors | ForEach-Object { "$_/https://github.com/$repo/releases/download" })
    $latestUrls = @($latestUrl)
    if (-not $env:BERTH_INSTALL_LATEST_URL) {
        # The API and the redirect are different hosts; keep both.
        $latestUrls += "https://github.com/$repo/releases/latest"
    }
    $latestUrls += @($mirrors | ForEach-Object { "$_/https://github.com/$repo/releases/latest" })

    if ($env:OS -ne 'Windows_NT') {
        throw 'this installer is for Windows; use install.sh on macOS or Linux'
    }

    $architecture = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture
    switch ($architecture) {
        'X64' { $arch = 'amd64' }
        'Arm64' { $arch = 'arm64' }
        default { throw "unsupported architecture: $architecture" }
    }

    if (-not $Version) {
        foreach ($candidate in $latestUrls) {
            try {
                $response = Invoke-WebRequest -Uri $candidate -UseBasicParsing -MaximumRedirection 5 `
                    -TimeoutSec 20 -Headers @{ 'User-Agent' = 'option-berth-installer' }
            }
            catch {
                continue
            }
            $tag = $null
            try { $tag = ($response.Content | ConvertFrom-Json).tag_name } catch { }
            if (-not $tag) {
                # A mirror answers releases/latest with a redirect to the tag page.
                $last = $response.BaseResponse
                $finalUri = if ($last.PSObject.Properties['ResponseUri']) { $last.ResponseUri } else { $last.RequestMessage.RequestUri }
                if ($finalUri) { $tag = "$finalUri".TrimEnd('/').Split('/')[-1] }
            }
            if ($tag -and $tag -match '^v?\d+\.\d+\.\d+$') {
                $Version = $tag
                break
            }
        }
        if (-not $Version -and -not $env:BERTH_INSTALL_LATEST_URL) {
            # jsDelivr mirrors the repository file and is reachable from hosts
            # where the GitHub API is not.
            try {
                $body = Invoke-RestMethod -Uri "https://cdn.jsdelivr.net/gh/$repo@main/VERSION" `
                    -UseBasicParsing -TimeoutSec 20
                if ("$body".Trim() -match '^\d+\.\d+\.\d+$') { $Version = "v$("$body".Trim())" }
            }
            catch { }
        }
        if (-not $Version) {
            throw 'could not resolve the latest release from any source; pass -Version vX.Y.Z'
        }
    }
    if ($Version -notmatch '^v?\d+\.\d+\.\d+$') {
        throw "invalid version: $Version"
    }
    if ($Version -notmatch '^v') {
        $Version = "v$Version"
    }

    if (-not $Dir) {
        $Dir = if ($env:LOCALAPPDATA) { Join-Path $env:LOCALAPPDATA 'option-berth\bin' } else { Join-Path $HOME '.local\bin' }
    }
    $DataDir = if ($env:BERTH_INSTALL_DATA_DIR) {
        $env:BERTH_INSTALL_DATA_DIR
    }
    elseif ($env:LOCALAPPDATA) {
        Join-Path $env:LOCALAPPDATA 'option-berth'
    }
    else {
        Join-Path $HOME '.local\share\option-berth'
    }

    function Invoke-Download {
        param([string]$Relative, [string]$Destination, [int]$TimeoutSec)
        foreach ($base in $assetBases) {
            $url = "$base/$Relative"
            Write-Host "install:   $url"
            try {
                Invoke-WebRequest -Uri $url -OutFile $Destination -UseBasicParsing -TimeoutSec $TimeoutSec
                return $true
            }
            catch {
                if (Test-Path -LiteralPath $Destination) {
                    Remove-Item -LiteralPath $Destination -Force -ErrorAction SilentlyContinue
                }
            }
        }
        return $false
    }

    $archive = "option-berth-$Version-windows-$arch.zip"
    $tmp = Join-Path ([IO.Path]::GetTempPath()) ('berth-install-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        Write-Host "install: $repo $Version (windows/$arch)"
        $archivePath = Join-Path $tmp $archive

        # Fetch the sidecar first. The official one is authoritative; a mirror
        # checksum must be confirmed by a second, different source before the
        # archive from that mirror is trusted.
        $expected = $null
        $index = 0
        foreach ($base in $assetBases) {
            $sumPath = Join-Path $tmp "checksum.$index"
            $index++
            try {
                Invoke-WebRequest -Uri "$base/$Version/$archive.sha256" -OutFile $sumPath `
                    -UseBasicParsing -TimeoutSec 20
            }
            catch { continue }
            $line = ([string](Get-Content -Raw $sumPath)).Trim()
            if ($line -notmatch '^([0-9a-fA-F]{64})\s+\*?(.+)$') { continue }
            $digest = $Matches[1]
            $recorded = $Matches[2]
            if ($recorded -ne $archive) { continue }
            if (-not $expected) {
                $expected = $digest
                if ($base -eq $baseUrl) { break }
            }
            elseif ($digest -ne $expected) {
                throw "checksum sources disagree for $archive: $base reports $digest"
            }
            else {
                break
            }
        }
        if (-not $expected) {
            throw 'could not download a valid checksum from any source'
        }

        if (-not (Invoke-Download -Relative "$Version/$archive" -Destination $archivePath -TimeoutSec 30)) {
            Write-Host 'install: no source was fast enough; retrying without the short timeout'
            if (-not (Invoke-Download -Relative "$Version/$archive" -Destination $archivePath -TimeoutSec 900)) {
                throw "could not download $archive from any source"
            }
        }
        $actual = (Get-FileHash -Algorithm SHA256 -Path $archivePath).Hash
        if ($actual -ne $expected) {
            throw "SHA256 mismatch for $archive"
        }

        Expand-Archive -Path $archivePath -DestinationPath $tmp -Force
        $root = Join-Path $tmp "option-berth-$Version-windows-$arch"
        foreach ($name in 'oberth.exe', 'jev-attention.exe') {
            if (-not (Test-Path -LiteralPath (Join-Path $root $name) -PathType Leaf)) {
                throw "archive is missing $name"
            }
        }

        # A running executable is locked on Windows; refuse before replacing
        # either file so the install cannot end up half old and half new.
        $running = @(Get-Process -Name 'oberth', 'jev-attention' -ErrorAction SilentlyContinue)
        if ($running.Count) {
            throw 'oberth is running; stop its services before installing'
        }

        New-Item -ItemType Directory -Force -Path $Dir | Out-Null
        foreach ($name in 'oberth.exe', 'jev-attention.exe') {
            $target = Join-Path $Dir $name
            try {
                Copy-Item -LiteralPath (Join-Path $root $name) -Destination $target -Force
            }
            catch {
                throw "could not replace $target; stop any running oberth service and try again"
            }
        }

        # Keep the licence and per-target notices beside the installed binaries.
        New-Item -ItemType Directory -Force -Path $DataDir | Out-Null
        $licence = Join-Path $root 'LICENSE'
        if (Test-Path -LiteralPath $licence -PathType Leaf) {
            Copy-Item -LiteralPath $licence -Destination (Join-Path $DataDir 'LICENSE') -Force
        }
        $notices = Join-Path $root 'licenses'
        if (Test-Path -LiteralPath $notices -PathType Container) {
            New-Item -ItemType Directory -Force -Path (Join-Path $DataDir 'licenses') | Out-Null
            Copy-Item -Path (Join-Path $notices '*') -Destination (Join-Path $DataDir 'licenses') -Recurse -Force
        }
        $manifest = Join-Path $root 'build-manifest.json'
        if (Test-Path -LiteralPath $manifest -PathType Leaf) {
            Copy-Item -LiteralPath $manifest -Destination (Join-Path $DataDir 'build-manifest.json') -Force
        }
    }
    finally {
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }

    Write-Host "installed  $Dir"
    Write-Host "notices    $DataDir"
    if ($NoModifyPath -or $env:NO_MODIFY_PATH -eq '1') {
        Write-Host "PATH       not modified. Add this directory to PATH: $Dir"
    }
    else {
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        $entries = @($userPath -split ';' | Where-Object { $_ })
        if ($entries -notcontains $Dir) {
            [Environment]::SetEnvironmentVariable('Path', (($entries + $Dir) -join ';'), 'User')
            Write-Host "PATH       added $Dir to the user PATH - open a new terminal to use it"
        }
        else {
            Write-Host "PATH       $Dir is already on the user PATH - nothing changed"
        }
    }

    Write-Host ''
    $installed = Join-Path $Dir 'oberth.exe'
    & $installed version
    if ($LASTEXITCODE -ne 0) {
        throw "installed $installed, but it did not run"
    }
}

try {
    Install-Oberth
}
catch {
    throw "install failed: $($_.Exception.Message)"
}
