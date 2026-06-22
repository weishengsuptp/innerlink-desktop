# innerlink-desktop cleanup script
#
# One-shot helper that nukes everything innerlink-related on
# this box so you can replace the binary, reinstall, or wipe
# state without hunting through 4 directories by hand.
#
# What it removes:
#   1. Running processes (innerlink-desktop + WebView2 children
#      spawned by Go's go-webview2 binding).
#   2. Data dir:  %APPDATA%\innerlink\
#      (device.key, aliases.json, chat.enc, roster.json,
#       innerlink.log, received/) - this is what os.UserConfigDir
#      in app.go's desktopPaths() resolves to on Windows.
#   3. WebView2 user data:
#      %LOCALAPPDATA%\github.com.weishengsuptp.innerlink-desktop\
#      (Wails uses the Go module path as the bundle id by default;
#      this is where WebView2 keeps cookies, cache, GPU shader
#      cache, crashpad data).
#   4. Build artifacts (only if -BuildArtifacts is passed):
#      build\bin\, frontend\dist\, frontend\node_modules\
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File cleanup.ps1
#   powershell -ExecutionPolicy Bypass -File cleanup.ps1 -BuildArtifacts
#
# Reinstall the binary after running this and you'll get a
# fresh device identity (new SM2 key pair) - all peers will
# see you as a brand new node. If you want to keep your
# identity, copy %APPDATA%\innerlink\device.key aside first
# and drop it back after cleanup.

[CmdletBinding()]
param(
    [switch]$BuildArtifacts
)

$ErrorActionPreference = 'SilentlyContinue'

function Remove-IfExists($path) {
    if (Test-Path $path) {
        Write-Host "rm $path"
        Remove-Item -Path $path -Recurse -Force
    }
}

# 1. Kill running instances. innerlink-desktop may have
#    spawned msedgewebview2.exe children that don't show up
#    under our name; kill anything whose parent is ours too.
Write-Host "==> killing running innerlink-desktop processes"
$ours = Get-Process -Name 'innerlink-desktop' -ErrorAction SilentlyContinue
foreach ($p in $ours) {
    Write-Host "    pid $($p.Id)  $($p.MainWindowTitle)"
    Stop-Process -Id $p.Id -Force
}

# WebView2 children inherit a different process name
# (msedgewebview2.exe). Find them by PPID.
$webview = Get-CimInstance Win32_Process -Filter "Name = 'msedgewebview2.exe'"
foreach ($w in $webview) {
    $ppid = $w.ParentProcessId
    foreach ($p in $ours) {
        if ($ppid -eq $p.Id) {
            Write-Host "    killing WebView2 child pid $($w.ProcessId) (parent was our pid $($p.Id))"
            Stop-Process -Id $w.ProcessId -Force
        }
    }
}

# Belt and suspenders: a second sweep in case Wails left
# anything behind on shutdown.
Start-Sleep -Milliseconds 500
Get-Process -Name 'innerlink-desktop' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue

# 2. App data dir.
$dataDir = Join-Path $env:APPDATA 'innerlink'
Write-Host "==> removing $dataDir"
Remove-IfExists $dataDir

# 3. WebView2 user data. Bundle id is the Go module path
#    by default (see wails.json -> 'info.productName' or the
#    Go module's import path). We clean both candidates:
#    the default (module-path based) and the simpler
#    product-name based.
$wvCandidates = @(
    Join-Path $env:LOCALAPPDATA 'github.com.weishengsuptp.innerlink-desktop'
    Join-Path $env:LOCALAPPDATA 'innerlink-desktop'
)
foreach ($c in $wvCandidates) {
    Write-Host "==> removing $c"
    Remove-IfExists $c
}

# 4. Build artifacts (optional).
if ($BuildArtifacts) {
    $repoRoot = Split-Path -Parent $PSScriptRoot
    $artifacts = @(
        Join-Path $repoRoot 'build\bin'
        Join-Path $repoRoot 'frontend\dist'
        Join-Path $repoRoot 'frontend\node_modules'
    )
    foreach ($a in $artifacts) {
        Write-Host "==> removing $a"
        Remove-IfExists $a
    }
}

Write-Host ""
Write-Host "done. you can paste / reinstall innerlink-desktop.exe now."