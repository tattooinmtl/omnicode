# Simulate user typing "omnicode" in a fresh terminal
# This proves the new UX works: omnicode launches the IDE

$ErrorActionPreference = "Continue"

Write-Host "=== User flow: open a fresh terminal and type 'omnicode' ===" -ForegroundColor Cyan
Write-Host ""
Write-Host "user@machine MINGW64 ~$ omnicode" -ForegroundColor Green
Write-Host ""

# Set HOME so the Rust REPL doesn't bail (Windows terminals don't have HOME natively)
$env:HOME = $env:USERPROFILE
# Use omnicode directly with --print to simulate a real launch without TTY
$exe = "$env:USERPROFILE\.omnicode\bin\omnicode.exe"
& $exe --print "hi" 2>&1 | Select-Object -First 15
Write-Host ""
Write-Host "(exit code: $LASTEXITCODE)"
Write-Host ""
Write-Host "user@machine MINGW64 ~$" -ForegroundColor Green
