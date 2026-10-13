# OmniCode Go code audit

**Scope:** the eight Go files in `omnicode/go/` (`luncher.go`, `db.go`, `server.go`,
`provider_state.go`, `settings.go`, `synckeys.go`, `verifykeys.go`,
`provider_state_test.go`), totalling roughly 5,500 lines.

**Method:** read every file in full, ran `gofmt -l`, `go vet`, `go build`,
`go test`, and reproduced the one suspicious runtime path I spotted in
`saveLearnedSkill` with a small standalone test program (since deleted).

**Verdict at a glance:** the code compiles, `go vet` is clean, and the test
suite passes. But the surface area is much wider than the test coverage
suggests, and I found one real bug, several near-misses, and a long list of
style/maintainability issues. The system-prompt composer alone is ~1.5 KB
of hard-coded instruction injected into every chat request, so the
"personality" of the agent lives in a single Go function — worth its own
review.

Severity tags: 🔴 bug · 🟠 risk · 🟡 smell · 🟢 note.

---

## 🔴 1. `saveLearnedSkill` is broken — the whole self-learning distiller is dead

**Location:** `omnicode/go/server.go`, near the bottom of the file
(`saveLearnedSkill`).

The function does:

```go
_, err := tx.Exec(`INSERT INTO skills (id, name, description, source, path, enabled)
    VALUES (?, ?, ?, 'learned', ?, 1)
    ON CONFLICT(id) DO UPDATE SET
        body_text = excluded.body_text,
        path      = excluded.path,
        enabled   = 1`,
    "learned_"+name, name, "...", skillPath)
```

The `skills` table (defined in `db.go`) has no `body_text` column. The
ON CONFLICT clause references `excluded.body_text`, which SQLite rejects
with `no such column: excluded.body_text`. The error is silently dropped
at the call site (`_ = saveLearnedSkill(...)`), so the chat handler
succeeds but the distiller never writes anything.

I reproduced this against the actual `skills` schema:

```
first insert err: SQL logic error: no such column: excluded.body_text (1)
conflict insert err: SQL logic error: no such column: excluded.body_text (1)
```

The first insert fails too (because SQLite parses the whole statement at
prepare time), so no `learned_skills` rows or `~/.omnicode/skills/learned/*.md`
files ever land. The in-source comment says this is "deliberately tolerant"
— it isn't. The feature has been dead since the moment it was written.

**Suggested fix:** drop the bogus `body_text` line from the SET clause.
The body lives on disk and in `learned_skills.body`, so the skills table
mirror only needs `path` and `description` updated:

```go
ON CONFLICT(id) DO UPDATE SET
    description = excluded.description,
    path        = excluded.path,
    enabled     = 1
```

Also, fold both writes (the `learned_skills` insert and the `skills`
mirror) into a single transaction so a partial failure doesn't leave
them out of sync. The current `withDB(...) ; withDB(...)` pair is two
separate transactions.

---

## 🔴 2. Single-connection DB blocks the whole server under concurrent load

**Location:** `omnicode/go/db.go`, `openDB` does `db.SetMaxOpenConns(1)`.

The comment says this is intentional: modernc.org/sqlite is safe with one
connection. That keeps writes simple, but `readDB` and `withDB` both call
`openDB()` on every operation and immediately `db.Close()` on the way
out. Under any concurrency that means:

- every request opens, runs the full `schemaSQL` (eight `CREATE TABLE`s
  plus indexes), then closes;
- the global `dbMu` mutex serializes *all* reads and writes through a
  single SQL handle;
- the IDE serves the editor on the same DB as the chat persistence and
  the audit log, so a long-running chat blocks file-tree refreshes.

The right pattern is: open the DB once at startup, keep a `*sql.DB` for
the process lifetime, run `schemaSQL` once, then write through a
`dbMu`-protected transaction helper but read concurrently. Modernc.org/sqlite
supports concurrent readers when opened in WAL mode
(`?_pragma=journal_mode(WAL)`) and `SetMaxOpenConns(n>1)`.

This is currently masked because the editor is single-user and most
requests are tiny, but the chat path runs claw-core in a goroutine while
the same DB is serving `/api/audit` polling, so it surfaces quickly.

**Suggested fix:** store the `*sql.DB` in a package-level var, init
once in `init()`, and either run schema in a `sync.Once` or rely on
`IF NOT EXISTS` plus a startup `db.Exec(schemaSQL)`. Set
`SetMaxOpenConns(8)` and enable WAL.

---

## 🟠 3. The system prompt is a 1.5 KB string literal embedded in `server.go`

**Location:** `omnicode/go/server.go`, `omnicodeSystemPrompt`.

The function builds a ~1.5 KB XML block — agent loop, tool discipline,
memory rules, conversation hygiene, "what should I do next?" — and
fprintf's it onto the front of every prompt. The text includes the
literal phrase "stupid answers" and the directive "Never reply with the
file contents in the chat". That is the IDE's personality living inside
a Go function, with the same edit-compile-restart cycle as the rest of
the code.

That has two consequences:

- Updating the agent's behavior is a code change. Marketing-grade edits
  ("use the word 'roughly' before approximations") become PRs.
- The prompt ships into every conversation, costing tokens. On long
  sessions with many turns that is measurable.

**Suggested fix:** load the prompt from `~/.omnicode/system.md` (or
ship a default in `assets/`) at startup, cache it, and let the editor
preview/edit it. Same for the post-turn "Step complete" rubric.

---

## 🟠 4. `applyProviderEnv` knows the name of one specific vendor

**Location:** `omnicode/go/provider_state.go`, `applyProviderEnv`.

The "openai" branch hardcodes `MINIMAX_API_KEY`, `MINIMAX_BASE_URL`, and
in the next line `XAI_*`. That's fine for the current provider set
(MiniMax M3, xAI Grok, etc.) but is a real footgun the moment a new
provider shows up. If someone adds a `cerebras` provider, the env
variables claw-core reads will still be the three above, and the new
provider will be silently misrouted to whichever one matches first.

**Suggested fix:** drive the env-var fan-out from the provider's kind.
Add a small map (kind → []string{envVar}) and let `applyProviderEnv`
read from it. Then new providers get env wiring for free, and the
caller doesn't need to know about MiniMax at all.

---

## 🟠 5. `chatStreamHandler` and `chatHandler` race over shared state

**Location:** `omnicode/go/server.go`, `serveChat`, `chatHandler`,
`chatStreamHandler`.

`serveChat` keeps mutable `reasoning`, `activity`, `toolEvents`, and
`toolUses` slices behind a `traceMu sync.Mutex`. The mutex is held
during appends, which is correct, but:

- The `live` callback (SSE forwarder) is invoked *outside* the lock,
  which is good, but the callback captures references to the same
  slices. A second call to `serveChat` would write to its own copies
  because they are local — but the `note` closure references local
  variables, so that's actually fine. I flagged it because the
  structure is fragile: the next person to add a shared field will
  probably forget the lock.
- The `rememberProvider` / `rememberModel` calls at the end of
  `serveChat` write through `setSetting` which opens its own DB
  transaction, and that can race with the rest of the request. Setting
  in a fire-and-forget goroutine after returning the response would be
  safer; the current order is correct but tightly coupled.

**Suggested fix:** wrap the sticky-state mutations in a separate small
function called explicitly, and document that `note` must be the only
way to touch the four trace slices.

---

## 🟠 6. `pickPort` is racy and the fallback lies

**Location:** `omnicode/go/luncher.go`, `pickPort`.

```go
for p := 4450; p < 4500; p++ {
    l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
    if err == nil { _ = l.Close(); return p }
}
return 4450
```

Two problems:

1. **Race window.** The kernel can hand the same port to another
   process between the test-listen and the real `ListenAndServe`. The
   server will then fail to bind and exit. The user sees a crash,
   not a retry.
2. **Silent fallback.** If every port 4450–4499 is taken, the function
   returns 4450 anyway, and the launcher will fail at boot with an
   opaque "address already in use".

**Suggested fix:** retry `srv.ListenAndServe` once on `EADDRINUSE`
with a different port, and print a clear error if all 50 are taken.
Or ask the kernel for a free port (`net.Listen("tcp", "127.0.0.1:0")`,
then read the bound port from `l.Addr()`).

---

## 🟠 7. `openaiBaseURL` builds a `b + "/v1"` twice for one case

**Location:** `omnicode/go/provider_state.go`, `openaiBaseURL`.

```go
if strings.HasSuffix(lowered, "/openai") {
    return b + "/v1"
}
return b + "/v1"
```

The two arms of this if are identical, so the condition is dead. A
refactor that adds a new suffix above will silently break that case
because both branches will keep matching.

**Suggested fix:** delete one branch.

---

## 🟠 8. Errors parsed with regex instead of the JSON the gateway already sent

**Location:** `omnicode/go/provider_state.go`, `suggestedModel`
and `affordableTokens`.

```go
var useInsteadRe = regexp.MustCompile(`(?i)use\s+"([^"]+)"\s+instead`)
var affordRe    = regexp.MustCompile(`(?i)afford\s+(\d+)`)
```

The OpenAI/OpenRouter error envelope is already structured JSON
(`{"error":{"message":"Use \"other-id\" instead","code":...}}`).
Parsing it with regex means a slightly different wording
("Please use foo-bar instead" or "use `foo-bar` instead" with backticks)
silently breaks the model's self-healing. The `assistantParts` and
`completeChatTokensParts` functions already decode the body with
`json.Unmarshal`; the error struct should be parsed the same way.

**Suggested fix:** unmarshal the error body into
`{Error struct{ Message string; Code string }}` and act on that, with
the regex as a fallback only.

---

## 🟠 9. `safeJoin` does a half-Windows check

**Location:** `omnicode/go/server.go`, `safeJoin`.

The function does `strings.ReplaceAll(rel, "\\", "/")` then
`filepath.Clean` then a series of `..` and `/../` checks, then
`filepath.Join(root, clean)`. On Windows:

- The path is normalized to forward slashes.
- `filepath.Clean` on Windows then re-converts and may interpret
  `C:\foo` again — but at that point `filepath.IsAbs(clean)` should
  catch it.
- The real problem: `filepath.Join("C:\\project", "/v1/file")` on
  Windows produces `C:\project\v1\file`, not what you'd expect from
  the Unix-style input. The current checks pass, but the behaviour
  is implicit and easy to break.

**Suggested fix:** run the same logic on `filepath.ToSlash(rel)` and
`path.Join` (Unix semantics) and only use `filepath` for the final
`EvalSymlinks` and `Join` with the resolved root. Or unit-test the
function with `path_separator=;` cases on Windows in CI.

---

## 🟠 10. `chatProbeModels` returns up to 3 ids by sorting *backwards*

**Location:** `omnicode/go/server.go`, `chatProbeModels`.

```go
added := 0
for i := len(ids) - 1; i >= 0 && added < 3; i-- {
    add(ids[i])
    if len(out) > before { added++ }
}
```

The comment explains that catalogs list retired ids first, so the *last*
ids are the ones the endpoint still serves. This is a real heuristic,
but it's also wrong for providers that sort the other way (Anthropic's
own catalog, Ollama, anything alphabetical). A better probe is to ask
the endpoint for a cheap health check on the candidate, or to prefer
id suffixes the gateway clearly flags as live (e.g. `-latest`).

**Suggested fix:** combine "last 3" with a per-id ping (`max_tokens=1,
messages=[probe]`) and pick the first that does not 404. Costs one
extra round-trip per probe but actually returns a working model id.

---

## 🟡 11. `gofmt` is unhappy with `server.go`

`gofmt -l` reports `server.go` as the only non-canonical file in the
package. The diff is hundreds of lines of "if x { y; z }" being expanded
to "if x {\n\ty\n\tz\n}". The package still compiles, but every future
PR will fight `gofmt` and `gofmt -s`.

**Suggested fix:** `gofmt -w server.go` once and add a CI check
(`gofmt -l . | (! grep .)`).

---

## 🟡 12. Massive `server.go` (3,889 lines) hides handlers in noise

Every REST handler is in one file, with no grouping beyond
`// ====` comment banners. The `include_learned` branch in
`listSkillsHandler` is 90 lines, the `serveChat` function is 280, and
the file ops (`mkfile`, `mkdir`, `rename`, `move`, `remove`, `zip`,
`share`, `openwith`, `convert`, `saveas`) are ten near-identical
helpers. A new contributor cannot find anything.

**Suggested fix:** split into `server.go` (router + meta),
`handlers_secrets.go`, `handlers_projects.go`, `handlers_files.go`,
`handlers_chat.go`, `handlers_skills.go`. The current file is bigger
than the entire Rust `claw-cli` crate.

---

## 🟡 13. Test coverage is 1 file out of 8

`provider_state_test.go` (357 lines) is the *only* test file. The
remaining ~5,200 lines have zero direct test coverage. Several
functions are non-trivial (the model-alias resolver, the URL
normalizers, `safeJoin`, `openaiBaseURL`, the skill distill
sanitizer) and would benefit from table tests.

**Suggested fix:** add `db_test.go` for `withDB`/`readDB` and
`migrateLegacySecretsDB`, `settings_test.go` for `resolveModelID` /
`resolveAgainst`, `server_test.go` for the file-tree walker and
`safeJoin` (the security-critical ones), and a `serveChat` integration
test that uses `httptest.Server` as a fake provider.

---

## 🟡 14. `nowString` and `nowTime` are two helpers in two files

`db.go` defines `nowString() string` returning RFC3339.
`settings.go` defines `nowTime() time.Time` returning `time.Now()`,
plus a wrapper `timeSince`. The `invalidateAgentCache` function isn't
called from anywhere in the package — the comment says "edits apply
without a restart" but the cache TTL is one second anyway, so the
invalidation path is dead.

**Suggested fix:** consolidate into one `internal/time` helper, and
remove `invalidateAgentCache` (or wire it into `cmdSyncKeys` and the
file-watch loop, whichever is intended).

---

## 🟡 15. `distillLearnedSkills` and `saveLearnedSkill` swallow errors

`distillLearnedSkills` runs in a goroutine after the chat response is
sent, and uses a `defer recover()` to swallow panics. `saveLearnedSkill`
returns an error, but every call site does `_ = saveLearnedSkill(...)`.
That means a corrupted database row would silently kill the whole
self-learning path — which is exactly what bug #1 is doing today.

**Suggested fix:** at least log to stderr on the first error per
session, so the operator can see why the distiller is not producing
files.

---

## 🟡 16. `convertFile` only handles three formats and returns a poor error

`md → txt`, `txt → md`, `json → csv`. For anything else the function
returns `"no local converter for %s (install pandoc / a toolchain)"`.
That message is shown to the user verbatim, so they think OmniCode is
recommending they install pandoc — when really the design just
declined to support other formats. Either ship pandoc, or make the
error message clearly say "this build doesn't include a converter
for X".

---

## 🟡 17. `safeJoin` does the symlink check, but only when both resolve

```go
absResolved, err := filepath.EvalSymlinks(abs)
if err == nil {
    rootResolved, rErr := filepath.EvalSymlinks(root)
    if rErr == nil {
        // ...compare...
    }
}
return abs, nil
```

If `EvalSymlinks(abs)` fails (the file doesn't exist yet, e.g. on
`writeFile` of a new file), the function falls through and returns
`abs` without the symlink check. The `os.MkdirAll` and `os.WriteFile`
calls then operate on the unverified path. In practice a symlinked
project root is rare, but `writeFileHandler` is exactly the
"create new file under project" path, and it would happily write
through a symlink the attacker placed in the project.

**Suggested fix:** if `EvalSymlinks(abs)` errors, at least verify that
the *parent directory* of `abs` resolves inside `root`. That catches
the symlink-in-tree case.

---

## 🟢 18. Things that are good

- `provider_state_test.go` has solid table tests for the URL
  normalizers and the OpenAI chat client. These are the parts most
  likely to silently regress against a new gateway.
- `safeJoin`'s prefix-of-resolved-root check is the right shape; it
  just needs the gap above fixed.
- The `migrateLegacySecretsDB` is a one-shot, idempotent, and
  non-fatal — the right call.
- The DB schema is consistent: every domain table uses
  `(id, created_at, updated_at)`, every domain has an audit row.
- `omnicodeSystemPrompt` is well-commented and the agent loop is
  self-describing — that's a real maintainability win.
- `failoverProvider` correctly skips the failing provider and prefers
  `s.DefaultProvider` over alphabetical alternatives.
- `completeChatTokensParts` retries on `402 Payment Required` with the
  budget the gateway said it could afford. Nice touch.

---

## Suggested fix order

If you only have time for a few of these, do them in this order:

1. **Bug #1** (one-line SQL fix, restores the self-learning distiller).
2. **Risk #6** (port-picker) and **Risk #2** (single-conn DB), because
   they're the only two issues that will visibly bite under normal use.
3. **Risk #3** (move the system prompt to a file), because every future
   agent-behaviour tweak should not be a code change.
4. **Risk #4** (kill the MiniMax name from `applyProviderEnv`).
5. Style/smell items whenever there's a quiet hour.

Everything else is good code that just needs more test coverage and a
gofmt pass.
