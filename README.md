# OmniCode

OmniCode is a local coding IDE that runs in the browser. A Go server serves the UI, stores settings in SQLite, and launches the Rust agent (`claw-core`) against the project you have open.

The app is built for Windows. The integrated terminal uses ConPTY.

## What you can do

- Chat with the agent from the Chat page, or from the editor sidebar.
- Open a project in the editor. Monaco shows the files, and `plan.md` is the session trace.
- In the editor, the left sidebar is the agent chat for that project. The settings list is behind the gear at the right end of the footer, next to the green connected light.
- Open a terminal on the current project folder, drag it inside the window, and dock it left, right, or bottom.
- On the Chat page, the bottom bar is a console, the prompt, and a second terminal on the same project.
- Point the agent at Anthropic or any OpenAI-compatible endpoint. Keys stay in the SQLite `secrets` table.
- Enable MCP servers and skills in the settings pages. The next chat turn publishes enabled MCP servers to the agent and sends the open file and tabs.

## Layout

| Path | What it is |
| --- | --- |
| `go/` | The luncher and the web API. `go build -o omnicode.exe .` from this folder. |
| `ui/` | The IDE. Static HTML, CSS, and JavaScript. No frontend build. |
| `rust/` | The agent workspace. `cargo build --release` produces the core the IDE launches as `claw-core.exe`. |
| `src/` | The earlier Python harness port. It is kept in the tree. The running IDE does not start it. |
| `install.ps1` | Builds the Go server, copies the UI into `%USERPROFILE%\.omnicode`, and installs a built Rust binary when one is already in `rust/target/release/`. |
| `system.md` | Default agent instructions. The running install copies this idea to `%USERPROFILE%\.omnicode\system.md`, which you can edit. The next chat turn reads it. |
| `docs/` | Audits and the parity notes. `docs/GO-AUDIT.md` is the Go review. The fixes from that review are in `go/`. |

The Rust crates (`api`, `runtime`, `tools`, `commands`, `plugins`, `lsp`, `server`, `claw-cli`, `compat-harness`) are the same sources as the clawd-code tree this app was built from. There was no remaining source diff to merge. The web IDE calls `claw-core` for a chat turn. It does not yet replace the Go server with `rust/crates/server`, and Monaco does not speak LSP yet.

## Install

Requires Go, and Rust if you want to rebuild the agent.

```powershell
git clone https://github.com/tattooinmtl/omnicode.git
cd omnicode
powershell -ExecutionPolicy Bypass -File install.ps1
```

The installer writes the app to `%USERPROFILE%\.omnicode` and adds `%USERPROFILE%\.omnicode\bin` to your user PATH. Open a new terminal after that.

If `rust\target\release\claw.exe` is not there yet, build it and copy it onto the name the server expects:

```powershell
cd rust
cargo build --release
copy target\release\claw.exe "$env:USERPROFILE\.omnicode\bin\claw-core.exe"
```

The crate binary may be named `claw.exe` or `claw-cli.exe` depending on the package. The server looks for `claw-core.exe` in the install `bin` folder.

## Run

```text
omnicode                         Open the IDE in the browser
omnicode web --port 4595         Choose a port
omnicode web --port 4595 --no-browser
omnicode start                   Rust REPL in this terminal
omnicode help
```

The server binds to `127.0.0.1`. With no `--port`, it takes the first free port from 4450 through 4499 and keeps that listener. A pinned port that is already taken stops the process with an error.

## Agent instructions and secrets

Edit `%USERPROFILE%\.omnicode\system.md` to change the standing instructions. Workspace, open file, tabs, enabled skills, and enabled MCP servers are added on each turn by the server.

Do not commit `agent/settings.json` or `omnicode.db`. Both can hold live API keys. This repo ignores them.

## Development

```powershell
cd go
go test ./...
go build -o "$env:USERPROFILE\.omnicode\bin\omnicode.exe" .
```

Restart `omnicode.exe` after a Go change. A refresh is enough for a change under `ui/`, because the server serves that folder as static files when you run from the install directory.
