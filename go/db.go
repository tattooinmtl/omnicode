// OmniCode SQLite layer
// All persistent state (secrets, settings, projects, MCP, plugins, skills,
// providers, sessions) lives in a single SQLite3 database at
// ~/.omnicode/omnicode.db. The old secrets.db file is migrated in.

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// dbMu serializes writes. Readers use the shared pool and do not take it.
// The pool stays open for the process; WAL lets a chat write proceed while
// the file tree and the audit log read.
var (
	dbMu     sync.Mutex
	poolOnce sync.Once
	pool     *sql.DB
	poolErr  error
)

// openDB returns the process-wide database pool, opening it on first use.
func openDB() (*sql.DB, error) {
	poolOnce.Do(func() {
		pool, poolErr = connectDB()
	})
	return pool, poolErr
}

// closeSharedDB drops the pool so tests can point config.Database elsewhere.
func closeSharedDB() {
	dbMu.Lock()
	defer dbMu.Unlock()
	if pool != nil {
		_ = pool.Close()
		pool = nil
	}
	poolOnce = sync.Once{}
	poolErr = nil
}

func connectDB() (*sql.DB, error) {
	if err := os.MkdirAll(config.InstallPath, 0755); err != nil {
		return nil, err
	}
	// Migrate the legacy secrets.db into the new omnicode.db if present.
	if err := migrateLegacySecretsDB(); err != nil {
		// non-fatal; the new DB will just start empty
		fmt.Fprintf(os.Stderr, "warning: legacy secrets migration: %v\n", err)
	}
	db, err := sql.Open("sqlite", sqliteDSN(config.Database))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	if err := db.Ping(); err != nil {
		// Some Windows paths need the plain filename instead of a file: URI.
		_ = db.Close()
		db, err = sql.Open("sqlite", config.Database)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(8)
		db.SetMaxIdleConns(8)
		_, _ = db.Exec(`PRAGMA busy_timeout=5000`)
		_, _ = db.Exec(`PRAGMA journal_mode=WAL`)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// sqliteDSN opens one file with WAL and a busy timeout on every connection.
func sqliteDSN(path string) string {
	return "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
}

// migrateLegacySecretsDB moves any rows from the old ~/.omnicode/secrets.db
// into the new omnicode.db secrets table, then leaves the old file in place
// (the user can delete it manually). One-time, on first run.
func migrateLegacySecretsDB() error {
	oldPath := filepath.Join(config.InstallPath, "secrets.db")
	if _, err := os.Stat(oldPath); err != nil {
		return nil // no legacy file
	}
	old, err := sql.Open("sqlite", oldPath)
	if err != nil {
		return err
	}
	defer old.Close()
	// Check if it has the secrets table at all
	row := old.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='secrets'")
	var n int
	if err := row.Scan(&n); err != nil || n == 0 {
		return nil
	}
	rows, err := old.Query("SELECT key, value FROM secrets")
	if err != nil {
		return err
	}
	defer rows.Close()
	// Open the new DB and insert (idempotent because we use OR IGNORE)
	newDB, err := sql.Open("sqlite", config.Database)
	if err != nil {
		return err
	}
	defer newDB.Close()
	if _, err := newDB.Exec(schemaSQL); err != nil {
		return err
	}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			continue
		}
		_, _ = newDB.Exec("INSERT OR IGNORE INTO secrets (key, value) VALUES (?, ?)", k, v)
	}
	return nil
}

// schemaSQL is the full OmniCode database schema. All tables use the same
// id/created_at/updated_at pattern for consistency.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS secrets (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  key TEXT NOT NULL UNIQUE,
  value TEXT NOT NULL,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS providers (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,           -- 'anthropic' | 'openai' | 'xai' | 'custom'
  base_url TEXT,
  api_key_ref TEXT,             -- FK to secrets.key (the actual key lives there)
  default_model TEXT,
  enabled INTEGER DEFAULT 1,
  metadata TEXT,                -- JSON blob
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  path TEXT NOT NULL UNIQUE,
  description TEXT,
  default_provider_id TEXT,
  default_model TEXT,
  permission_mode TEXT DEFAULT 'workspace-write',
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (default_provider_id) REFERENCES providers(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS project_files (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  rel_path TEXT NOT NULL,
  abs_path TEXT NOT NULL,
  size INTEGER,
  sha256 TEXT,
  indexed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, rel_path),
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS mcp_servers (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  transport TEXT NOT NULL,      -- 'stdio' | 'websocket' | 'http' | 'sdk' | 'managed_proxy'
  command TEXT,
  args TEXT,                    -- JSON array
  url TEXT,
  enabled INTEGER DEFAULT 1,
  config TEXT,                  -- JSON blob
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS plugins (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  version TEXT,
  source TEXT,                  -- 'local' | 'git' | 'registry'
  install_path TEXT,
  enabled INTEGER DEFAULT 1,
  metadata TEXT,                -- JSON blob
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS skills (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT,
  source TEXT,                  -- 'user' | 'project' | 'legacy'
  path TEXT,
  enabled INTEGER DEFAULT 1,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS agents (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT,
  model TEXT,
  reasoning_effort TEXT,
  source TEXT,
  system_prompt TEXT,
  metadata TEXT,                -- JSON blob
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  project_id TEXT,
  title TEXT,
  started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  ended_at DATETIME,
  message_count INTEGER DEFAULT 0,
  token_input INTEGER DEFAULT 0,
  token_output INTEGER DEFAULT 0,
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS session_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  role TEXT NOT NULL,           -- 'user' | 'assistant' | 'system' | 'tool'
  content TEXT,
  tool_name TEXT,
  tool_call_id TEXT,
  ts DATETIME DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts DATETIME DEFAULT CURRENT_TIMESTAMP,
  actor TEXT,
  action TEXT NOT NULL,
  target TEXT,
  details TEXT
);

CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project_id);
CREATE INDEX IF NOT EXISTS idx_session_messages_session ON session_messages(session_id);
CREATE INDEX IF NOT EXISTS idx_project_files_project ON project_files(project_id);
CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_log(ts);

-- editor_tabs persists which files the IDE has open for a given session, and
-- in what order. The first tab (position 0) for a fresh session is always
-- plan.md; subsequent tabs are added by the creation pipeline whenever the
-- assistant calls write_file. Reopening a session rehydrates the tabs.
CREATE TABLE IF NOT EXISTS editor_tabs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  path TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'file',   -- 'file' | 'plan'
  position INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(session_id, path, kind),
  FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_editor_tabs_session ON editor_tabs(session_id);

-- editor_plans stores the per-session plan.md content. The brainstorming
-- trace lives here; nothing in this row is ever deleted by the user.
CREATE TABLE IF NOT EXISTS editor_plans (
  session_id TEXT PRIMARY KEY,
  content TEXT NOT NULL DEFAULT '',
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

-- memory_rememberings holds user-pinned facts that survive across sessions.
-- Anything the agent should never forget ("the user prefers tabs not spaces",
-- "this project's stack is X") goes here. /api/memory/search indexes into it.
CREATE TABLE IF NOT EXISTS memory_rememberings (
  id TEXT PRIMARY KEY,
  content TEXT NOT NULL,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_memory_rememberings_created ON memory_rememberings(created_at);

-- learned_skills is generated from WebSearch / WebFetch tool outputs. The
-- distiller writes a row when an answer comes back, then writes the skill
-- body into a SKILL.md file under ~/.omnicode/skills/learned/<name>.md so
-- the existing skills loader can pick it up.
CREATE TABLE IF NOT EXISTS learned_skills (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  query TEXT NOT NULL,
  body TEXT NOT NULL,
  source TEXT,            -- 'websearch' | 'webfetch'
  url TEXT,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_learned_skills_created ON learned_skills(created_at);
`

// withDB runs fn inside a serialized write transaction. Use this for any write.
// The shared pool stays open.
func withDB(fn func(*sql.Tx) error) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// readDB runs fn against the shared pool. It does not take dbMu, so reads
// can overlap a write while SQLite is in WAL mode.
func readDB(fn func(*sql.DB) error) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	return fn(db)
}

// getSetting reads a key from the settings table. Missing keys return "".
func getSetting(key string) string {
	var v string
	_ = readDB(func(db *sql.DB) error {
		return db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	})
	return v
}

// setSetting upserts a key in the settings table.
func setSetting(key, value string) error {
	return withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value,
				updated_at = CURRENT_TIMESTAMP`, key, value)
		return err
	})
}

// audit appends a row to the audit log.
func audit(action, target, details string) {
	_ = withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO audit_log (actor, action, target, details) VALUES (?, ?, ?, ?)",
			"omnicode", action, target, details)
		return err
	})
}

// nowString returns the current time formatted for the DB.
func nowString() string { return time.Now().UTC().Format(time.RFC3339) }

// jdump / jload are small helpers for JSON columns.
func jdump(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
func jload(s string, out any) error {
	if s == "" {
		return nil
	}
	return json.Unmarshal([]byte(s), out)
}
