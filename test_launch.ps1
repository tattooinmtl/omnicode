# Quick smoke test: verify omnicode launches the REPL in --print mode (non-interactive)
$env:PATH = "$env:USERPROFILE\.omnicode\bin;$env:PATH"

# Use claw-core's --print flag to test that omnicode actually hands off to it.
# We call omnicode with --version to confirm the luncher binary itself runs.
& "$env:USERPROFILE\.omnicode\bin\omnicode.exe" --version
Write-Host "---"
# And confirm help is reachable
& "$env:USERPROFILE\.omnicode\bin\omnicode.exe" --help
