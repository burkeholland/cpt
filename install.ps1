# cpt installer for Windows/PowerShell
# Usage: irm https://raw.githubusercontent.com/burkeholland/cpt/main/install.ps1 | iex
$ErrorActionPreference = 'Stop'

$repo = "burkeholland/cpt"
$installDir = if ($env:CPT_INSTALL_DIR) { $env:CPT_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "cpt" }

# Detect architecture
$arch = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
    'X64'   { 'amd64' }
    'Arm64' { 'arm64' }
    default { throw "Unsupported architecture: $_" }
}

# Get latest version
$release = Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest"
$version = $release.tag_name -replace '^v', ''

$url = "https://github.com/$repo/releases/download/v$version/cpt_windows_$arch.zip"
Write-Host "Downloading cpt v$version for windows/$arch..."

# Download and extract
$tmp = New-Item -ItemType Directory -Path (Join-Path ([System.IO.Path]::GetTempPath()) ([System.Guid]::NewGuid()))
try {
    $zipPath = Join-Path $tmp "cpt.zip"
    Invoke-WebRequest -Uri $url -OutFile $zipPath -UseBasicParsing
    Expand-Archive -Path $zipPath -DestinationPath $tmp -Force

    # Install
    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    Copy-Item (Join-Path $tmp "cpt.exe") (Join-Path $installDir "cpt.exe") -Force
} finally {
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

Write-Host ""
Write-Host "✓ cpt v$version installed to $installDir\cpt.exe" -ForegroundColor Green
Write-Host ""

# Check if install dir is in PATH
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($userPath -notlike "*$installDir*") {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$installDir", 'User')
    $env:Path = "$env:Path;$installDir"
    Write-Host "Added $installDir to your PATH." -ForegroundColor Yellow
    Write-Host "Restart your terminal for PATH changes to take effect." -ForegroundColor Yellow
    Write-Host ""
}

# Register the shell widget (Ctrl+K keybinding)
& (Join-Path $installDir "cpt.exe") --install

Write-Host ""
Write-Host "Restart your terminal to activate Ctrl+K." -ForegroundColor Cyan
