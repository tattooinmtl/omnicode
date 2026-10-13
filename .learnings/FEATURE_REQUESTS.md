# Feature Requests

User-requested capabilities.

## Entries

## [FEAT-20251012-001] editor_view_with_tabs_and_creation_pipeline

**Logged**: 2025-10-12T00:00:00Z
**Priority**: critical
**Status**: in_progress
**Area**: frontend

### Requested Capability
Turn the current settings-panel dashboard (`omnicode/ui/`) into a real coding
IDE by adding an Editor view, a tab system for open files, a file tree for the
active project workspace, and a creation pipeline that always opens with
`plan.md` as the first tab and then auto-appends tabs in the order the
assistant creates files (`index.html`, `style.css`, `main.js`, …).

### User Context
Symptom: opening the app shows unformatted text in chat, no code blocks, no
IDE. The user wants clawd-code's tools (read/write/edit/bash) to be visible
and the files they create to open automatically as tabs in the editor.

### Complexity Estimate
complex

### Suggested Implementation
- Add `Editor` to the sidebar nav in `omnicode/ui/index.html`.
- New `views.editor` in `omnicode/ui/static/app.js` backed by Monaco via AMD
  CDN (`cdn.jsdelivr.net/npm/monaco-editor`).
- Tab strip across the top of the right rail; tabs persist per session via
  `GET/PUT /api/tabs`.
- File tree (left rail inside Editor) backed by `GET /api/files?project_id=…`.
- Plan pane: on every `POST /api/sessions`, server pre-creates `plan.md` for
  the session's project; tab 1 is always `plan.md`.
- New chat cards post-process assistant `tool_uses` of type `write_file` and
  append tabs in arrival order.
- Add `GET /api/files`, `PUT /api/files/{id}`, `GET /api/plan`,
  `PUT /api/plan`, `GET /api/tabs`, `POST /api/tabs`, `DELETE /api/tabs/{id}`
  to `omnicode/go/server.go`.

### Metadata
- Frequency: first_time
- Related Features: clawd-code `read_file`/`write_file`/`edit_file` tools in
  `rust/crates/tools/src/lib.rs`
- See Also: AUDIT.md (full audit)

## [FEAT-20251012-002] markdown_render_and_syntax_highlight_in_chat

**Logged**: 2025-10-12T00:00:00Z
**Priority**: critical
**Status**: in_progress
**Area**: frontend

### Requested Capability
Assistant messages in the chat pane must render markdown (headings, lists,
inline code, fenced code blocks, links) and fenced code blocks must be
syntax-highlighted by language. Required to make the IDE usable; today
`renderMessage` uses `.textContent` and flattens everything to plain text.

### User Context
"crap load of none formated text no code blocks" — chat is unreadable for
anything but a one-line reply.

### Complexity Estimate
simple

### Suggested Implementation
- Load `marked` (`cdn.jsdelivr.net/npm/marked/marked.min.js`) and
  `highlight.js` core + a dark theme (`cdn.jsdelivr.net/npm/highlight.js/styles/atom-one-dark.css`)
  via `<link>` and `<script>` in `omnicode/ui/index.html`.
- Replace `el.querySelector('.content').textContent = m.content` with a
  marked render and an `hljs.highlightElement` pass over each `<pre><code>`.
- CSS: code blocks dark bg, rounded corners, copy-to-clipboard button.

### Metadata
- Frequency: first_time
- See Also: AUDIT.md
