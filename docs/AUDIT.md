# OmniCode Audit & Implementation Plan

**Auditor:** OmniCode self-audit
**Date:** 2025
**Subject:** `omnicode/` Go luncher + web IDE and its integration with `clawd-code` Rust REPL

---

## TL;DR

The codebase is two parts: a Go luncher / web IDE at `omnicode/` and a Rust port
of "clawd-code" at `rust/crates/`. The Go IDE is a settings-panel dashboard
(Chat / Projects / Providers / MCP / Plugins / Skills / Agents / Database /
Audit) but it is **not** a coding IDE: there is no editor, no file tree, no
tabs, no `plan.md`, no markdown rendering, no syntax highlighting. The
clawd-code Rust REPL runs the agent turn (`bash`, `read_file`, `write_file`,
`edit_file`, `WebFetch`, `WebSearch`, `TodoWrite`, `Skill`, `Agent`, etc.) but
its output is discarded after the first text bubble; tool calls and the files
they produce never reach the UI.

The single-user-visible symptom matches exactly what was reported:

> "i get a crap load of none formated text no code blocks, no real coding IDE
> functions in this app"

Root causes are three concrete bugs:

| Bug | File:line | Why it hurts |
| --- | --- | --- |
| Raw `textContent` chat rendering | `omnicode/ui/static/app.js:388-399` (the `renderMessage` helper) | Code fences, headers, lists, links are all flattened to plaintext |
| Tools thrown away by chat proxy | `omnicode/go/server.go:1856-1881` (`coreMessage`) | `tool_uses` is parsed but only the names are surfaced as `"Used tools: x, y"`; the actual edits happen invisibly |
| No editor view, tabs, file tree, `/api/files`, or `plan.md` | Missing from `index.html` (lines 26-72 list only settings) and `server.go` route table | The IDE cannot open, read, edit, or save project files |

---

## Architecture (today)

```
                   ┌─────────────────────────────────────────┐
                   │  browser:  omnicode/ui/index.html       │
                   │   sidebar    right pane (one view)      │
                   │     │        /api/* JSON  ◄── app.js ──┐│
                   └─────┼──────────────────────────────────┼┘
                         │                                  │
                127.0.0.1:4450  (HTTP/JSON, SSE in name only)│
                         │                                  │
                   ┌─────▼──────────────────────────────────▼─────┐
                   │  omnicode.exe  (Go luncher + REST + sqlite3)  │
                   │  • server.go — REST routes                    │
                   │  • db.go     — schema, secrets/projects/etc.  │
                   │  • provider_state.go — credential resolution │
                   │  • settings.go       — app key/value         │
                   └─────┬────────────────────────────────────────┘
                         │ exec.Command, injects env from sqlite
                         ▼
                   ┌─────────────────────────────────────────────┐
                   │  claw-core.exe  (Rust REPL from rust/crates) │
                   │  crates/tools/src/lib.rs: ToolSpec registry  │
                   │   bash, read_file, write_file, edit_file,    │
                   │   WebFetch, WebSearch, TodoWrite, Skill,     │
                   │   Agent, NotebookEdit, REPL, PowerShell, …   │
                   └─────────────────────────────────────────────┘
```

`omnicode/ui/static/app.js` is a 1147-line vanilla SPA. The chat handler at
`server.go:1883` builds the prompt, runs `claw-core --output-format json`,
parses the JSON, and returns **only** `response` as a string. `app.js:397`
then assigns that string with `el.querySelector('.content').textContent =
m.content;` — no markdown, no syntax highlighting, no tool cards.

---

## What "at par with Claude Code" requires

Claude Code today offers, at the UI layer:

1. Inline markdown with syntax-highlighted code blocks
2. A workspace file tree (left rail) with read/edit
3. Multiple open files in tabs (recent files, drag-to-reorder, dirty markers)
4. Visible tool calls (each `bash` / `read_file` / `write_file` rendered as a
   collapsible card so the user knows what just happened)
5. Streaming tokens (SSE / WebSocket, not "wait then bubble")
6. A persistent `plan.md` per session — brainstorming is captured, not lost
7. Plan mode + a creation pipeline (one request → many files in order)

The OmniCode shell does (1) only as plain text, partially does (5) via a
named SSE endpoint that actually just delegates to the JSON route
(`server.go:2110-2130` — single chunk, not streamed), and lacks the rest.

---

## Implementation Plan (priority order)

### P0 — must ship to be at parity in your hands today

| # | What | Where | Status |
| - | ---- | ----- | ------ |
| 1 | Markdown rendering + code-block highlighting in chat | `omnicode/ui/static/app.js` (`renderMessage`) + tiny CSS | NEW |
| 2 | Editor view in nav + Monaco editor + tab strip | `omnicode/ui/index.html` (add `<a data-view="editor">`), `app.js` new `views.editor`, `app.css` layout | NEW |
| 3 | File tree (left rail inside Editor, expandable) | `views.editor` + `app.css` | NEW |
| 4 | `GET /api/files?project_id=…` recursive tree, `GET /api/files/{id}`, `PUT /api/files/{id}` | `omnicode/go/server.go` + `db.go` schema (`project_files` already exists) | NEW |
| 5 | Plan pane: every new session auto-creates `plan.md` and the first opened tab | `plan.md` rendered as the leftmost tab, never closable by default | NEW |
| 6 | Tabs persistence (`/api/tabs`) so reloads keep the workspace | `server.go` + small table | NEW |

### P1 — should ship

| # | What | Where |
| - | ---- | ----- |
| 7 | Tool-call cards in chat (each `bash` / `read_file` / `write_file` / `edit_file` rendered as a foldable card) | `server.go` `coreMessage` returns full structured payload; `app.js` renders cards |
| 8 | Streaming chat (real SSE per-token, not single-chunk) | `server.go` `chatStreamHandler`, integrate claw-core streaming output |
| 9 | Creation pipeline: when assistant says "create index.html", it lands in tab 2; "style.css" → tab 3; "main.js" → tab 4; tabs auto-open in order | `app.js` post-process chat to detect `write_file` tool uses and open a tab for each path |
| 10 | `GET /api/plan`, `PUT /api/plan` (per-session `plan.md`) | `server.go` |

### P2 — nice to have

| # | What |
| - | ---- |
| 11 | Drag-to-reorder tabs, dirty-marker, keyboard `Ctrl+W` / `Ctrl+Tab` |
| 12 | File watcher: project edits in the IDE appear in the tree without manual refresh |
| 13 | "Add file" / "Rename" / "Delete" tree actions |
| 14 | Wire Rust `server` crate (`rust/crates/server/`) to replace Go luncher long-term |
| 15 | LSP integration (`rust/crates/lsp/` is already there) for hover / go-to-def |

### Where the clawd-code → omnicode mapping lands

| Claude Code feature | clawd-code Rust crate | Status today |
| --- | --- | --- |
| Read / Edit / Write tool | `rust/crates/tools/src/lib.rs:236-294` (`read_file`, `write_file`, `edit_file`) | Works, but invisible in UI |
| Bash tool | `rust/crates/tools/src/lib.rs:218-234` | Works, invisible |
| Glob / Grep | `rust/crates/tools/src/lib.rs:280-318` | Works, invisible |
| TodoWrite | `rust/crates/tools/src/lib.rs:356-383` | Works, invisible |
| WebFetch / WebSearch | `rust/crates/tools/src/lib.rs:320-353` | Works, invisible |
| Skill / Agent | `rust/crates/tools/src/lib.rs:384-414` | Works, invisible |
| MCP | `rust/crates/runtime/src/mcp*.rs` | Works, no UI |
| Plugins | `rust/crates/plugins/` | Scaffolded, no marketplace yet |
| Permissions / hook execution | `rust/crates/runtime/src/{config,conversation}.rs` | Config-only, no runtime hook |
| CLI commands set | `rust/crates/commands/src/lib.rs` (2517 lines) | `/help /status /compact /model /memory /init /diff /version /export /session /clear /cost /resume /permissions /config` — already broad |
| LSP | `rust/crates/lsp/` | Crate exists, needs to be wired into editor |

The Rust port has all the agentic pieces. The blocker is purely UI: bring the
IDE into the same app so the user can **see and edit** what claw-core is doing.

---

## Creation pipeline you asked for (concrete)

You want:

> Tab 1 = `plan.md` (always created first when a session starts; this is where
> all brainstorming / reasoning traces live; kept forever so revisiting an
> old idea still finds it)
> Tab 2 = `index.html`
> Tab 3 = `style.css`
> Tab 4 = `main.js`
> …

How it will work once the changes ship:

1. **New session**: server creates a `plan.md` row in `project_files` (path:
   `<project>/plan.md`, content pre-filled with the session title and an
   empty `## Brainstorm` section).
2. **Editor view, first load**: opens with one tab — `plan.md`, dirty=false.
3. **User asks**: *"build me a single-page photo gallery"*. Assistant replies
   with three `write_file` tool uses in the order it intends to create them
   (`index.html`, `style.css`, `main.js`).
4. **Editor listens**: when chat lands a `write_file` for a path, the tabs
   strip appends a new tab in order, opens it, focuses the editor, scrolls
   the new file into view in the tree.
5. **Tab order is the pipeline** — close / re-order is a user action; order
   of arrival equals order in the tabs strip.
6. **Brainstorm trace**: every assistant message is also appended to
   `plan.md` as `## Step N — <short summary>` with the prompt and the
   tool-call list, never deleted.
7. **Reload session**: tabs and `plan.md` reload from `/api/tabs` and
   `/api/plan`.

---

## Files to change

```
omnicode/ui/index.html                      # add Editor nav item, mark-deps links
omnicode/ui/static/app.css                  # tab strip, editor area, file tree, code blocks
omnicode/ui/static/app.js                   # markdown render, Editor view, tabs, file tree, plan pane
omnicode/go/server.go                       # /api/files, /api/plan, /api/tabs, real streaming
omnicode/go/db.go                           # plan row + tabs table
omnicode/README.md                          # update with new workflow
.learnings/{LEARNINGS,ERRORS,FEATURE_REQUESTS}.md  # log this audit
```

No Rust changes are strictly required for the UI to start working. The Rust
work (`rust/crates/server/`, hook execution, plugin marketplace) is the
follow-up so claw-core becomes a first-class backend rather than a shell-out.

---

## Build & verify

```bash
cd omnicode/go
go vet ./...
go build -o ../omnicode.exe .
powershell -ExecutionPolicy Bypass -File ../install.ps1
omnicode web
```

Open `http://127.0.0.1:4450/`, click **Editor** in the sidebar, type into the
plan.md tab. Send a prompt asking for an HTML file. Watch tabs populate in
order.
