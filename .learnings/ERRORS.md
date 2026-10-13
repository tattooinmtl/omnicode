# Errors

Command failures, exceptions, tool errors.

## Entries

## [ERR-20251012-001] chat_renders_plain_text_no_markdown

**Logged**: 2025-10-12T00:00:00Z
**Priority**: critical
**Status**: in_progress
**Area**: frontend

### Summary
`omnicode/ui/static/app.js:388-399` (`renderMessage`) sets message content via
`el.querySelector('.content').textContent = m.content;`, so every markdown
construct (fenced code blocks, inline code, lists, headers) is flattened to
plaintext. The user sees "a crap load of none formated text".

### Error

```js
function renderMessage(m) {
  const el = document.createElement('div');
  el.className = 'msg ' + m.role;
  el.innerHTML = `
    <div class="avatar">${m.role === 'user' ? 'U' : 'AI'}</div>
    <div class="body">
      <div class="role">${m.role}${m.ts ? ' · ' + new Date(m.ts).toLocaleTimeString() : ''}</div>
      <div class="content"></div>
    </div>`;
  el.querySelector('.content').textContent = m.content;
  return el;
}
```

### Context
- Chat handler returns text from `claw-core --output-format json`.
- `coreMessage` in `omnicode/go/server.go:1856-1881` parses JSON `message` and
  `tool_uses` but discards `tool_uses` and only echoes back a `"Used tools: …"`
  string. So even if rendering fixed markdown, the user wouldn't see any
  edits-only spoken text.

### Suggested Fix
1. Render markdown via `marked` (CDN) and run `hljs.highlightElement` over
   every `<pre><code>`.
2. Change `coreMessage` to return a JSON envelope `{"message": …, "tool_uses":
   [{name, input, result?}]}` so the UI can render each invocation as a card.
3. Persist the same structured message in `session_messages` so reloads keep
   the tool history.

### Metadata
- Reproducible: yes
- Related Files:
  - omnicode/ui/static/app.js
  - omnicode/go/server.go
  - omnicode/ui/index.html
- See Also: AUDIT.md, FEAT-20251012-002

## [ERR-20251012-002] editor_view_missing

**Logged**: 2025-10-12T00:00:00Z
**Priority**: critical
**Status**: in_progress
**Area**: frontend

### Summary
The IDE has no editor view, no file tree, no tab system. Sidebar nav
(`omnicode/ui/index.html:26-72`) lists Chat / Projects / Sessions /
Providers / Endpoints / AI Control / MCP / Plugins / Skills / Agents /
Database / API Keys / App / Audit — none of which opens a file.

### Error

```text
$ curl -s http://127.0.0.1:4450/api/files | jq .
{
  "ok": false,
  "error": "404 page not found"
}
```

### Context
The clawd-code Rust REPL has full Read/Write/Edit/Bash tooling
(`rust/crates/tools/src/lib.rs:236-294`) but the Go luncher never exposes a
file-CRUD endpoint, and the UI never renders one. Tools run blind.

### Suggested Fix
1. Add `Editor` to the sidebar.
2. Add `GET /api/files?project_id=…` recursive tree, `GET /api/files/{id}`,
   `PUT /api/files/{id}` to `server.go`.
3. Render Monaco editor + tab strip + file tree in `views.editor`.
4. Make `plan.md` the first tab on every new session.

### Metadata
- Reproducible: yes
- Related Files:
  - omnicode/ui/index.html
  - omnicode/ui/static/app.js
  - omnicode/ui/static/app.css
  - omnicode/go/server.go
- See Also: AUDIT.md, FEAT-20251012-001

## [ERR-20251012-003] chat_stream_endpoint_does_not_stream

**Logged**: 2025-10-12T00:00:00Z
**Priority**: high
**Status**: pending
**Area**: backend

### Summary
`POST /api/chat/stream` (`omnicode/go/server.go:2110-2130`) is wired as a
server-sent-events endpoint, but it `Flush()`s a single "start" event and
then calls the non-streaming handler via `writeOK` — which writes a
non-event-stream body. So the response is malformed for SSE clients and only
ever contains one chunk.

### Error

```text
event: start
data: {}
{"ok":true,"data":{"status":"delegated to /api/chat"}}
```

### Context
Even with a polished UI, the chat has to wait for the full `claw-core`
process to exit before showing anything. Latency is blocked on the full agent
turn instead of token-level.

### Suggested Fix
- Pipe `claw-core --output-format stream-json` (or a future Rust streaming
  flag) directly through the SSE response, translating each JSON event into a
  matching `event:` token.
- Until that flag exists, chunk the assistant message in two halves and
  flush between them; better than one chunk and a five-second wait.
- Defer deeper work to `rust/crates/server` long-term.

### Metadata
- Reproducible: yes
- Related Files: omnicode/go/server.go
- See Also: AUDIT.md
