#Requires -Version 5.1
<#
.SYNOPSIS
Remove an option-berth CLI installed by install.ps1.

.DESCRIPTION
Stops the daemon if it can, removes oberth.exe + jev-attention.exe, removes the
notices install.ps1 left in %LOCALAPPDATA%\option-berth, and removes the install
directory from the user PATH. It keeps the %USERPROFILE%\.option-berth state
directory unless -PurgeState is passed.

.EXAMPLE
irm https://raw.githubusercontent.com/SheathedSharp/option-berth/main/uninstall.ps1 | iex

.EXAMPLE
.\uninstall.ps1 -Dir C:\tools\berth -PurgeState
#>
[CmdletBinding()]
param(
    [string]$Dir = $env:BERTH_INSTALL_DIR,
    [string]$DataDir = $env:BERTH_INSTALL_DATA_DIR,
    [switch]$KeepPath,
    [switch]$PurgeState
)

$ErrorActionPreference = 'Stop'

function Uninstall-Oberth {
    if ($env:OS -ne 'Windows_NT') {
        throw 'this uninstaller is for Windows; use uninstall.sh on macOS or Linux'
    }

    if (-not $Dir) {
        $Dir = if ($env:LOCALAPPDATA) { Join-Path $env:LOCALAPPDATA 'option-berth\bin' } else { Join-Path $HOME '.local\bin' }
    }
    if (-not $DataDir) {
        $DataDir = if ($env:LOCALAPPDATA) { Join-Path $env:LOCALAPPDATA 'option-berth' } else { Join-Path $HOME '.local\share\option-berth' }
    }
    $stateDir = if ($env:BERTH_HOME) { $env:BERTH_HOME } else { Join-Path $HOME '.option-berth' }

    $oberth = Join-Path $Dir 'oberth.exe'
    if (Test-Path -LiteralPath $oberth -PathType Leaf) {
        & $oberth daemon stop 2>$null | Out-Null
        Start-Sleep -Milliseconds 300
    }

    $running = @(Get-Process -Name 'oberth', 'jev-attention' -ErrorAction SilentlyContinue)
    if ($running.Count) {
        throw 'oberth is still running; stop its services before uninstalling'
    }

    $removed = @()
    foreach ($name in 'oberth.exe', 'jev-attention.exe') {
        $target = Join-Path $Dir $name
        if (Test-Path -LiteralPath $target -PathType Leaf) {
            Remove-Item -LiteralPath $target -Force
            $removed += $name
        }
    }
    if ($removed.Count) {
        Write-Host "removed    $($removed -join ', ') from $Dir"
    }
    else {
        Write-Host "removed    nothing from $Dir"
    }

    $noticeFiles = @((Join-Path $DataDir 'LICENSE'), (Join-Path $DataDir 'build-manifest.json'))
    $noticeDir = Join-Path $DataDir 'licenses'
    if (@($noticeFiles | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf }).Count -or (Test-Path -LiteralPath $noticeDir -PathType Container)) {
        foreach ($file in $noticeFiles) {
            Remove-Item -LiteralPath $file -Force -ErrorAction SilentlyContinue
        }
        Remove-Item -LiteralPath $noticeDir -Recurse -Force -ErrorAction SilentlyContinue
        if (-not (Get-ChildItem -LiteralPath $DataDir -Force -ErrorAction SilentlyContinue)) {
            Remove-Item -LiteralPath $DataDir -Force -ErrorAction SilentlyContinue
        }
        Write-Host "removed    notices from $DataDir"
    }

    if ($KeepPath) {
        Write-Host 'PATH       left alone (-KeepPath)'
    }
    else {
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        $entries = @($userPath -split ';' | Where-Object { $_ })
        $needle = $Dir.TrimEnd('\')
        $kept = @($entries | Where-Object { $_.TrimEnd('\') -ne $needle })
        if ($kept.Count -ne $entries.Count) {
            [Environment]::SetEnvironmentVariable('Path', ($kept -join ';'), 'User')
            Write-Host "PATH       removed $Dir from the user PATH - open a new terminal"
        }
        else {
            Write-Host 'PATH       no installer entry found; nothing changed'
        }
    }

    if ($PurgeState) {
        if (-not $stateDir) {
            throw 'refusing to remove an empty state directory'
        }
        $fullState = [IO.Path]::GetFullPath($stateDir)
        $fullHome = [IO.Path]::GetFullPath($HOME)
        if ($fullState -eq $fullHome -or $fullState -eq [IO.Path]::GetPathRoot($fullState)) {
            throw "refusing to remove $stateDir"
        }
        if (Test-Path -LiteralPath $stateDir) {
            Remove-Item -LiteralPath $stateDir -Recurse -Force
        }
        Write-Host "state      removed $stateDir"
    }
    elseif (Test-Path -LiteralPath $stateDir -PathType Container) {
        Write-Host "state      kept $stateDir (remove with -PurgeState)"
    }
}

try {
    Uninstall-Oberth
}
catch {
    throw "uninstall failed: $($_.Exception.Message)"
}
