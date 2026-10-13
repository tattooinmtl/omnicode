OmniCode Installation
=====================

Installation Directory: C:\Users\ThePa\.omnicode
Binaries: C:\Users\ThePa\.omnicode\bin
Secret Database: C:\Users\ThePa\.omnicode\secrets.db

Usage:
  omnicode                  - Show help
  omnicode scan             - Scan project dependencies
  omnicode secret-store <key> <value>  - Store a secret
  omnicode secret-retrieve <key>       - Retrieve a secret
  omnicode secret-list                 - List stored secrets
  omnicode repair                      - Apply repair protocols

To start the full OmniCode REPL, run:
  claw  (from the original clawd-code Rust binary)

Providers (Omni bridge)
-----------------------
claw-core only speaks the Anthropic API, so bin\start.bat starts a local
bridge (bridge\omni_bridge.py, port 47821) that routes to Omni's providers.
  start.bat --model <provider>/<model>     e.g. nvidia/nemotron-3-ultra-550b-a55b
  python bridge\omni_bridge.py --list      providers, models, aliases
  python bridge\omni_bridge.py --sync      refresh providers.json from Omni + live /models
providers.json holds no keys; they are read live from ~/.omni (settings.json, .env).
No --model = defaultModel in providers.json. Log: bridge\bridge.log
