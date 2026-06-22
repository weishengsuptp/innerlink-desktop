# innerlink-desktop cleanup script
#
# One-shot helper that nukes everything innerlink-related on
# this box so you can replace the binary, reinstall, or wipe
# state without hunting through 4 directories by hand.
#
# What it removes:
#   1. Running processes (innerlink-desktop + WebView2 children
#      spawned by Go's go-webview2 binding). This is now
#      unconditional — even if a process looks alive, we kill
#      it. If you ran this on your own session, the script
#      would terminate itself, but you wouldn't be reading
#      this output anyway.
#   2. Single-instance lockfile (we always delete it; the
#      script's whole point is to unstick a window that won't
#      re-open because the old WebView2 child held the lock).
#   3. Data dir:  %APPDATA%\innerlink\
#      (device.key, aliases.json, chat.enc, roster.json,
#       innerlink.log, received/) - this is what os.UserConfigDir
#      in app.go's desktopPaths() resolves to on Windows.
#   4. WebView2 user data:
#      %LOCALAPPDATA%\github.com.weishengsuptp.innerlink-desktop\
#      (Wails uses the Go module path as the bundle id by default;
#      this is where WebView2 keeps cookies, cache, GPU shader
#      cache, crashpad data).
#   5. Build artifacts (only if -BuildArtifacts is passed):
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

# 1. Kill every innerlink-desktop.exe, no questions asked.
#    This also catches orphan processes that survived a
#    crash or force-quit and would otherwise hold the
#    single-instance lock + the data-dir file handles.
Write-Host "==> killing running innerlink-desktop processes"
$ours = @(Get-Process -Name 'innerlink-desktop' -ErrorAction SilentlyContinue)
foreach ($p in $ours) {
    Write-Host "    pid $($p.Id)  $($p.MainWindowTitle)"
    Stop-Process -Id $p.Id -Force
}

# WebView2 children inherit a different process name
# (msedgewebview2.exe). Kill every msedgewebview2 whose
# parent was ours, then a broader sweep for any remaining
# webview2 with our module path in the command line (in
# case the parent already exited and they got reparented).
$webview = Get-CimInstance Win32_Process -Filter "Name = 'msedgewebview2.exe'"
$ourPids = $ours | ForEach-Object { $_.Id }
foreach ($w in $webview) {
    $ppid = $w.ParentProcessId
    $kill = $false
    if ($ourPids -contains $ppid) { $kill = $true }
    elseif ($w.CommandLine -like '*innerlink-desktop*') { $kill = $true }
    if ($kill) {
        Write-Host "    killing WebView2 child pid $($w.ProcessId) (ppid=$ppid)"
        Stop-Process -Id $w.ProcessId -Force
    }
}

# A second sweep after a short pause in case Wails left
# any reparented stragglers behind.
Start-Sleep -Milliseconds 500
Get-Process -Name 'innerlink-desktop' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue

# 2. Single-instance lockfile. Always delete it — the
#    whole reason this script exists is to unstick a
#    window that won't re-open because the old instance
#    held the lock and the new instance refuses to
#    steal it (it's < 1h old).
$lockPath = Join-Path $env:TEMP 'innerlink-desktop.lock'
Write-Host "==> removing $lockPath"
Remove-IfExists $lockPath

# 3. App data dir.
$dataDir = Join-Path $env:APPDATA 'innerlink'
Write-Host "==> removing $dataDir"
Remove-IfExists $dataDir

# 4. WebView2 user data. Bundle id is the Go module path
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

# 5. Build artifacts (optional).
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