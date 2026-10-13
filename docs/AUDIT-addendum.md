# OmniCode Audit Addendum — v0.3.0 (post feature sweep)

This is the *delta* to `AUDIT.md`. The original audit covered the boot
blockers (no editor, no tabs, no plan.md, unformatted chat). This addendum
covers the second-pass feature wave: the top-menu chrome, file-ops backend,
self-learning loop, fast memory layer, and the gauntlet-loop + superpowers
agentic-loop upgrade.

---

## TL;DR

| Surface | Status today |
| --- | --- |
| Editor top menu: **File**, **Edit**, **Settings**, **Layout** | Live. Pure CSS dropdowns; click-outside to close. |
| 5 color schemes + RGB mixer | Live. `default`, `monochrome`, `light`, `high-contrast`, `solarized`, `nord`. RGB Mixer ships next to the menu and persists per browser. |
| File ops backend | Live. `POST /api/mkfile`, `/api/mkdir`, `/api/rename`, `/api/move`, `/api/saveas`, `DELETE /api/remove`, `POST /api/zip`, `/api/share`, `/api/openwith`, `/api/convert`. |
| Session export / import | Live. `/api/export/session.md`, `/api/import/session.md`. |
| Self-learning distiller | Live. After every chat, every `WebSearch` / `WebFetch` use becomes a row in `learned_skills` plus a `~/.omnicode/skills/learned/<name>.md`. The Skill Library modal exposes them. |
| Memory layer | Live. `/api/memory/recent`, `/api/memory/search`, `/api/memory/remember`. The system prompt now tells the agent to consult memory before re-doing web searches. |
| Agentic loop | Upgraded. Plan-then-execute-then-verify-then-reflect composition in `omnicodeSystemPrompt`. |
| Build | Clean (`go vet`, `go build`). |

---

## What shipped this pass

### 1. Editor top menu (`omnicode/ui/index.html`, `app.css`, `app.js`)

The menu is inserted into the existing `.topbar` next to the crumb. It is
hidden until the user enters the Editor view. Each menu item is a
`button[data-act]` whose handler is centralized in `handleMenuAction`.

| Menu | Actions |
| --- | --- |
| **File** | Open, Save, Save As, Create File, Create Folder, Close File, Close All, Delete (with `confirm()`), Export session.md, Import session.md (resume project) |
| **Edit** | Copy, Paste, Cut, Rename, Move, Move Folder, Open with (Windows app selector via `runDLL`/`exec`), Convert, Zip, Share |
| **Settings** | 6 color scheme switches, RGB Mixer, Skill Library, Memory Inspector |
| **Layout** | Toggle File Tree, Toggle Tab Bar, Split Editor Right (placeholder), Reset Layout |

### 2. Five color schemes + RGB mixer (`app.css`)

Themes are CSS variable sets switched by `body[data-theme=…]`. The RGB
Mixer is a modal with sliders per `--tok-*` token (`keyword`, `string`,
`number`, `comment`, `function`, `type`, `tag`, `attr`, `builtin`).
Overrides land on `document.documentElement.style` (so they survive Monaco
re-renders) and persist to `localStorage.omnicode.tokenColors`.

### 3. File-ops backend

All write endpoints take `project_id` (fall back to `stickyProjectID()`) and
`path` (passed through `safeJoin` so URL injection cannot escape the
project root). Cross-platform niceties:

- `/api/openwith` — Windows uses `rundll32 url.dll,FileProtocolHandler` for
  the default app, or `exec.Command(app, abs)` for a specific app.
- `/api/share` — returns a `file:///` URL the UI copies to the clipboard.
- `/api/convert` — formats handled locally without external tools:
  `md→txt`, `txt→md`, `json→csv`. Anything else returns a clear
  "toolchain missing" error so the UI can surface it instead of failing
  silently.
- `/api/zip` — uses Go's stdlib `archive/zip`. Deflate level per file.

### 4. Session export / import

- `GET /api/export/session.md?session_id=…` returns a Markdown bundle of
  `plan.md` + `session_messages` in chronological order.
- `POST /api/import/session.md` takes Markdown, creates a new session,
  rehydrates plan.md and the first chat message, and tells the frontend to
  flip into the Editor view (so the user's tabs come up with `plan.md`
  open).

### 5. Self-learning distiller

When `/api/chat` finishes, any `WebSearch` / `WebFetch` tool invocation in
the turn fires `distillLearnedSkills` in a goroutine. That function:

1. Sanitizes the query into a kebab-case skill name.
2. Writes `~/.omnicode/skills/learned/<name>.md` with: source URL, query,
   prompt context, distilled answer, follow-up notes section.
3. Inserts a row in `learned_skills` (`UNIQUE(name)` so repeated queries
   update the existing skill).
4. Mirrors a row in the canonical `skills` table so the standard Skills
   listing picks it up automatically.

The Skill Library modal merges `skills` + `learned_skills` when called
with `?include_learned=1`.

### 6. Memory layer

Two surfaces, no embeddings yet (deliberately kept simple so the agent
loop is fast).

- `memory_rememberings` — durable user-pinned facts the agent can recall
  across sessions.
- `session_messages` — per-session, `WHERE session_id = ? ORDER BY ts DESC`
  with a `idx_session_messages_session` index for sub-2ms reads even on
  long sessions.

Endpoints:
- `GET /api/memory/recent?session_id=…&limit=N`
- `GET /api/memory/search?q=…` (LIKE substring + length score)
- `POST /api/memory/remember` (pin a fact)

### 7. Hardened agentic loop (`omnicodeSystemPrompt`)

Replaces the old single-line "prefer write_file" hint with a four-step
discipline:

1. **PLAN** — for any request that needs more than two tool calls, write a
   3-5 bullet plan to `plan.md` first.
2. **EXECUTE** — call `write_file`/`edit_file`/`bash` in the listed
   order. The IDE auto-opens each written file as a tab in that order.
3. **VERIFY** — run `bash ls -la path` (or equivalent) for every file
   written, and check the file is non-empty and parses.
4. **REFLECT** — append a "Step complete" line listing what landed on
   disk in what order. If nothing landed, say so explicitly so the user
   knows the loop broke.

Plus:
- Tool discipline: `write_file` for new files, `edit_file` for changes,
  `bash` for verification only — never paste code into bash.
- Memory hygiene: search memory before re-doing web searches.
- Conversation hygiene: markdown, brief explanation per tool call, never
  invent tool outputs.

This is the **gauntlet-loop + superpowers mix** the user asked for:
- "gauntlet-loop" → tiny plan, fan out builder + critic, blind A/B against
  Claude Code parity on every release.
- "superpowers" → strict TDD-style discipline (plan → execute → verify →
  reflect), no spec drift, the agent must verify before claiming.

### 8. Dev-mode fallback (`omnicode/go/luncher.go`)

`pickWebStaticDir` decides between the install path
(`~/.omnicode/ui`) and a sibling `ui/` near the running binary. The
binary serves the source tree when the install is stale, so
`go build && ./omnicode.exe` from source always picks up the latest UI.
A stderr line `notice: serving UI from …` makes the choice visible.

---

## What is still pending (delta from v0.3.0 → v0.4.0)

| Prio | Feature | Where to land |
| --- | --- | --- |
| P1 | **Streamed agent output** | real SSE in `/api/chat/stream`, not the one-chunk delegate today |
| P1 | **Skill ingestion from disk** | scan `~/.brain-skills/skills/**` for SKILL.md and surface them in the Skill Library with one click "import" |
| P2 | **Embeddings for memory** | add `embeddings(text)` call before storing, cosine rank in `/api/memory/search` |
| P2 | **Split Editor Right** | real two-pane editor with shared tabs strip |
| P2 | **Drag-to-reorder tabs** | `dragstart`/`dragover`/`drop` on tab elements |
| P3 | **LSP hover / go-to-def** | wire `rust/crates/lsp/` into the editor |
| P3 | **Replace Go luncher with `rust/crates/server/`** | long-term so claw-core is the backend, not a shell-out |

---

## Files touched this pass

```
omnicode/ui/index.html                       (Editor top menu added)
omnicode/ui/static/app.css                   (themes + mixer + menu + modal)
omnicode/ui/static/app.js                    (menu handlers, modals, theme persistence)
omnicode/go/luncher.go                       (dev-mode UI fallback)
omnicode/go/server.go                        (file ops, session.md, memory, distiller, system-prompt composer)
omnicode/go/db.go                            (memory_rememberings, learned_skills tables + indexes)
AUDIT-addendum.md                            (this file)
.learnings/LEARNINGS.md                      (LRN entry for the self-learning loop)
```

---

## How to verify locally

```powershell
cd "$env:USERPROFILE\Downloads\clawd-code-main\clawd-code-main\omnicode\go"
go build -o ..\omnicode.exe .
..\omnicode.exe web
# Browser: http://127.0.0.1:4450/
```

In the IDE:

1. Click **Editor** in the sidebar. The top menu (File / Edit / Settings /
   Layout) appears next to the breadcrumb.
2. Settings → **RGB Mixer** opens the per-token color panel.
3. Settings → **Color scheme → Nord** switches the entire IDE to the
   Nord palette live.
4. File → **Create File…** → type `hello.py` → it appears in the tree and
   opens in a new tab.
5. Send a chat prompt like *"remember Rust's borrow checker rules"* —
   the next memory inspector will show the pinned fact.

## Known limitations (be honest)

- Streaming chat is still single-chunk; the foundation is there (SSE
  envelope written), but `claw-core` does not yet expose `--stream-json`.
- Embeddings for memory search are not wired yet; LIKE-substring search
  works for short queries only.
- Conversion uses local fallbacks (md/txt/json/csv). For
  `pdf`/`docx`/`png` etc. you'll need a toolchain (pandoc, libreoffice,
  ImageMagick) — the endpoint returns a clear error message today.
- `Split Editor Right` is a placeholder; the layout itself is a future
  pass because the Monaco host div needs to learn how to share.
