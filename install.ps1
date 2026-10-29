#Requires -Version 5.1
<#
.SYNOPSIS
Install the released option-berth CLI on Windows without a Go toolchain.

.DESCRIPTION
Resolves the latest engine release, downloads the Windows archive for this
machine, verifies its SHA256 sidecar, and installs oberth.exe + jev-attention.exe
into %LOCALAPPDATA%\option-berth\bin. It starts no services and writes no Git data.

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
        $release = Invoke-RestMethod -Uri $latestUrl -UseBasicParsing -Headers @{ 'User-Agent' = 'option-berth-installer' }
        $Version = $release.tag_name
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

    $archive = "option-berth-$Version-windows-$arch.zip"
    $tmp = Join-Path ([IO.Path]::GetTempPath()) ('berth-install-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        Write-Host "install: $repo $Version (windows/$arch)"
        $archivePath = Join-Path $tmp $archive
        Invoke-WebRequest -Uri "$baseUrl/$Version/$archive" -OutFile $archivePath -UseBasicParsing
        $sumPath = "$archivePath.sha256"
        Invoke-WebRequest -Uri "$baseUrl/$Version/$archive.sha256" -OutFile $sumPath -UseBasicParsing

        $line = (Get-Content -Raw $sumPath).Trim()
        if ($line -notmatch '^([0-9a-fA-F]{64})\s+\*?(.+)$') {
            throw 'checksum file is malformed'
        }
        $expected = $Matches[1]
        $recorded = $Matches[2]
        if ($recorded -ne $archive) {
            throw "checksum names $recorded, expected $archive"
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
