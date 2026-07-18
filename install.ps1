# yaah installer for Windows
#   iwr -useb https://raw.githubusercontent.com/buchenberg/yaah/main/install.ps1 | iex

$ErrorActionPreference = "Stop"

$Repo = "buchenberg/yaah"

$Arch = $env:PROCESSOR_ARCHITECTURE
if ($Arch -eq "AMD64")   { $GoArch = "amd64" }
elseif ($Arch -eq "ARM64") { $GoArch = "arm64" }
else {
    Write-Error "unsupported architecture: $Arch"
    exit 1
}

$Binary = "yaah-windows-${GoArch}.exe"
$DownloadUrl = "https://github.com/${Repo}/releases/latest/download/${Binary}"

Write-Host "  ==> downloading yaah for windows/$GoArch..." -ForegroundColor Green
Write-Host "  $DownloadUrl"

$Tmp = [System.IO.Path]::GetTempPath()
$TmpFile = Join-Path $Tmp "yaah.exe"

try {
    Invoke-WebRequest -Uri $DownloadUrl -OutFile $TmpFile -UseBasicParsing
} catch {
    Write-Error "download failed — is the release binary uploaded?"
    exit 1
}

$InstallDir = Join-Path $env:LOCALAPPDATA "yaah"
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$Dest = Join-Path $InstallDir "yaah.exe"
Move-Item -Force $TmpFile $Dest

Write-Host "  installed yaah to $Dest" -ForegroundColor Green

$UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($UserPath -notlike "*$InstallDir*") {
    Write-Host -ForegroundColor Yellow "  WARN $InstallDir is not in your user PATH"
    Write-Host -ForegroundColor Yellow "  Add it in Settings > System > About > Advanced system settings > Environment Variables"
    Write-Host -ForegroundColor Yellow "  Or run:"
    Write-Host -ForegroundColor Yellow '    [Environment]::SetEnvironmentVariable("Path", [Environment]::GetEnvironmentVariable("Path", "User") + ";' + $InstallDir + '", "User")'
}

Write-Host ""
& $Dest version 2>$null || $true

if (-not (Test-Path "$env:USERPROFILE\.yaah\config.yaml")) {
    Write-Host "  ==> scaffolding config at ~/.yaah/config.yaml..." -ForegroundColor Green
    & $Dest config edit 2>$null || $true
}

Write-Host ""
Write-Host "  yaah is installed. Next steps:"
Write-Host "    yaah doctor           # check your setup"
Write-Host '    yaah config edit       # add your API key'
Write-Host "    yaah                  # start the REPL"
Write-Host '    yaah "your prompt"    # one-shot'
Write-Host ""
