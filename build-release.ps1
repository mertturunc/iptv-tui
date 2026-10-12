# Builds standalone release archives for iptv-tui across the platforms it
# supports. Each archive contains the single static binary — there is no
# runtime, registry, or DLL to install. The app locates its config/data via
# the OS per-user directory (Windows: %AppData%\iptv-tui, Linux: ~/.config,
# macOS: ~/Library/Application Support), so it works from a USB stick or a
# shared drive The only external program the TUI shells out to is mpv
#
# Usage (PowerShell 7+):
#   .\build-release.ps1            # version from git describe, fallback "dev"
#   $env:VERSION="1.2.3"; .\build-release.ps1 -Platform linux,darwin
param(
    [string[]]$Platform = @("windows", "linux", "darwin"),
    [string]$Arch = "amd64",
    [string]$Version = ""
)

$ErrorActionPreference = "Stop"
$repo = Split-Path -Parent $MyInvocation.MyCommand.Path
# Prefer go from PATH; allow override via GO_BIN env for custom installs.
$go = if ($env:GO_BIN) { $env:GO_BIN } else { "go" }
$dist = Join-Path $repo "dist"
New-Item -ItemType Directory -Force -Path $dist | Out-Null

if (-not $Version) {
    if (Get-Command git -ErrorAction SilentlyContinue) {
        $v = & git -C $repo describe --tags --always --dirty 2>$null
        if ($v) { $Version = $v }
    }
    if (-not $Version) { $Version = "dev" }
}
Write-Host "Version: $Version"

$archMap = @{
    "amd64" = "x86_64"
    "arm64" = "arm64"
    "386"   = "i386"
}

foreach ($os in $Platform) {
    $suffix = $archMap[$Arch]
    $base = "iptv-tui-$Version-$os-$suffix"
    $outDir = Join-Path $dist $base
    New-Item -ItemType Directory -Force -Path $outDir | Out-Null

    $env:GOOS = $os
    $env:GOARCH = $Arch
    $env:CGO_ENABLED = "0"

    $exe = if ($os -eq "windows") { "iptv-tui.exe" } else { "iptv-tui" }
    Write-Host "Building $os/$Arch -> $outDir"
    & $go build -trimpath -ldflags "-s -w -X main.version=$Version" -o (Join-Path $outDir $exe) .
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

    Set-Content -LiteralPath (Join-Path $outDir "README.txt") -Value @"
iptv-tui $Version ($os/$Arch)

A terminal IPTV client for M3U playlists and Xtream Codes portals.
Runs mpv as the media player. Single static binary -- no install.

Usage:
  $exe              launch the TUI
  $exe -play <url>  play a URL/file immediately with mpv, then exit
  $exe -add-source <m3u-url>   add a source to the config, then exit
  $exe -version     print the version, then exit

Data & cache: created on first run under
  Windows: %AppData%\iptv-tui
  Linux:   ~/.config/iptv-tui          (cache: ~/.cache/iptv-tui)
  macOS:   ~/Library/Application Support/iptv-tui

Dependencies: mpv must be on PATH (https://mpv.io). EPG/whole-list
snapshots are cached to disk so startup works offline and is instant.
"@ -Encoding UTF8

    Push-Location $dist
    Compress-Archive -Path $base -DestinationPath "$base.zip" -Force
    Pop-Location
    Remove-Item -Recurse -Force $outDir
    Write-Host "Packaged $base.zip"
}

"Done. Archives in $dist"
Get-ChildItem -LiteralPath $dist -Filter *.zip | Select-Object Name, Length, LastWriteTime

# Checksums for the release archives.
# Written in `sha256sum` format so `sha256sum -c` works on Linux/macOS.
$sums = Get-ChildItem -LiteralPath $dist -Filter *.zip | Sort-Object Name | ForEach-Object {
    "{0}  {1}" -f (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLower(), $_.Name
}
if ($sums) {
    $sumsPath = Join-Path $dist "SHA256SUMS"
    Set-Content -LiteralPath $sumsPath -Value $sums -Encoding ASCII
    Write-Host "Wrote $sumsPath"
    $sums | ForEach-Object { Write-Output "  $_" }
}
