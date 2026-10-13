# OmniCode Installer
# Builds the Go luncher + serves the web UI + installs the Rust core.

$ErrorActionPreference = "Stop"

$InstallDir = "$env:USERPROFILE\.omnicode"
$SourceDir  = Join-Path $PSScriptRoot "go"
$UiDir      = Join-Path $PSScriptRoot "ui"
$BinDir     = "$InstallDir\bin"
$InstallBin = "$InstallDir\bin"
$ScriptsDir = "$InstallDir\scripts"

Write-Host "=== OmniCode Installer ===" -ForegroundColor Cyan

# Directory structure
New-Item -ItemType Directory -Path $InstallDir  -Force | Out-Null
New-Item -ItemType Directory -Path $BinDir     -Force | Out-Null
New-Item -ItemType Directory -Path $ScriptsDir -Force | Out-Null
New-Item -ItemType Directory -Path $InstallDir\ui -Force | Out-Null
New-Item -ItemType Directory -Path $InstallDir\ui\static -Force | Out-Null

# 1. Go luncher
Write-Host "[1/6] Building Go luncher..."
Push-Location $SourceDir
try {
    if (-not (Test-Path "go.mod")) { go mod init omnicode | Out-Null }
    go get modernc.org/sqlite 2>$null | Out-Null
    go build -o "$BinDir\omnicode.exe" .
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }
} finally { Pop-Location }

# 2. Web UI (static files)
Write-Host "[2/6] Copying web UI..."
Copy-Item -Path "$UiDir\index.html"      -Destination "$InstallDir\ui\index.html" -Force
Copy-Item -Path "$UiDir\static\*"        -Destination "$InstallDir\ui\static\" -Force -Recurse

# 3. Rust core
Write-Host "[3/6] Installing Rust core (claw-core.exe)..."
$rustExe = Join-Path $PSScriptRoot "rust\target\release\claw.exe"
if (Test-Path $rustExe) {
    Copy-Item -Path $rustExe -Destination "$BinDir\claw-core.exe" -Force
    Write-Host "  Installed from $rustExe"
} else {
    Write-Host "  WARNING: $rustExe not found. Run: cd `"$PSScriptRoot\rust`" && cargo build --release" -ForegroundColor Yellow
}

# 4. Database (the Go server auto-creates this on first launch, so just verify)
Write-Host "[4/6] Verifying database path..."
$dbPath = "$InstallDir\omnicode.db"
if (-not (Test-Path $dbPath)) {
    # The Go luncher will create it. Trigger a quick start-up to migrate any legacy data.
    & "$BinDir\omnicode.exe" config 2>&1 | Out-Null
}
Write-Host "  Database: $dbPath"

# 5. PATH
Write-Host "[5/6] Adding $BinDir to user PATH..."
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$BinDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$BinDir", "User")
}

# 6. Wrapper for legacy REPL
$startBat = "@echo off`r`n`"$BinDir\claw-core.exe`" %*"
Set-Content -Path "$BinDir\start.bat" -Value $startBat
$omnicodeBat = "@echo off`r`n`"$BinDir\omnicode.exe`" %*"
Set-Content -Path "$BinDir\omnicode.bat" -Value $omnicodeBat

Write-Host ""
Write-Host "=== OmniCode installed at $InstallDir ===" -ForegroundColor Green
Write-Host ""
Write-Host "Open a NEW terminal and run:" -ForegroundColor Cyan
Write-Host "  omnicode                  - Open the OmniCode web IDE in your browser"
Write-Host "  omnicode web              - Same as above"
Write-Host "  omnicode web --port 9000  - Use a specific port"
Write-Host "  omnicode start            - Launch the legacy Rust REPL in this terminal"
Write-Host "  omnicode scan             - Scan project deps"
Write-Host "  omnicode secret-store K V - Store a secret"
Write-Host "  omnicode help             - Full command list"
Write-Host ""
Write-Host "Database: $dbPath" -ForegroundColor Yellow
Write-Host "UI dir:   $InstallDir\ui" -ForegroundColor Yellow
