package memory

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

// migrate creates the schema if it doesn't exist.
func (d *DB) migrate() error {
	// Fail fast when the database was written by a newer yaah whose
	// schema this binary cannot understand (review A6): an older binary
	// must not silently run half-supported queries against it.
	if _, err := d.sql.Exec(`
	CREATE TABLE IF NOT EXISTS schema_meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	`); err != nil {
		return fmt.Errorf("create schema_meta: %w", err)
	}
	var version string
	err := d.sql.QueryRow(`SELECT value FROM schema_meta WHERE key = 'version'`).Scan(&version)
	// dbVersion counts schema releases; 0 = a database that predates
	// version stamping (no row yet). The repairs below key off it.
	dbVersion := 0
	switch {
	case err == nil:
		// Numeric comparison — lexicographic ordering breaks at
		// multi-digit versions ("10" < "2" as strings).
		parsed, convErr := strconv.Atoi(version)
		if convErr != nil {
			return fmt.Errorf("invalid memory db schema version %q", version)
		}
		dbVersion = parsed
		if dbVersion > currentSchemaVersion {
			return fmt.Errorf(
				"memory db schema version %d is newer than this binary supports (%d); upgrade yaah",
				dbVersion, currentSchemaVersion)
		}
	case err != nil && err != sql.ErrNoRows:
		return fmt.Errorf("read schema version: %w", err)
	}

	schema := `
	CREATE TABLE IF NOT EXISTS sessions (
		id         TEXT PRIMARY KEY,
		started_at INTEGER NOT NULL,
		ended_at   INTEGER,
		cwd        TEXT,
		model      TEXT,
		tokens_in  INTEGER DEFAULT 0,
		tokens_out INTEGER DEFAULT 0,
		tokens_cached INTEGER DEFAULT 0,
		sub_tokens_in INTEGER DEFAULT 0,
		sub_tokens_out INTEGER DEFAULT 0,
		title           TEXT DEFAULT '',
		updated_at      INTEGER DEFAULT 0,
		-- mode is the persisted session mode ('build' or 'plan');
		-- '' is build, the zero value.
		mode            TEXT DEFAULT ''
	);

	CREATE TABLE IF NOT EXISTS messages (
		session_id        TEXT NOT NULL REFERENCES sessions(id),
		idx               INTEGER NOT NULL,
		role              TEXT NOT NULL,
		content           TEXT NOT NULL,
		reasoning_content TEXT DEFAULT '',
		reasoning_signature TEXT DEFAULT '',
		tool_name         TEXT,
		tool_call_id      TEXT,
		tool_calls        TEXT,
		ts                INTEGER NOT NULL,
		id                TEXT DEFAULT '',
		trace_id          TEXT DEFAULT '',
		turn_id           TEXT DEFAULT '',
		PRIMARY KEY (session_id, idx)
	);

	CREATE TABLE IF NOT EXISTS trace_owners (
		owner_id       TEXT PRIMARY KEY,
		parent_session TEXT NOT NULL,
		role           TEXT DEFAULT '',
		created_at     INTEGER NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_trace_owners_parent ON trace_owners(parent_session);

	CREATE TABLE IF NOT EXISTS memory (
		id          TEXT PRIMARY KEY,
		text        TEXT NOT NULL,
		tags        TEXT,
		source      TEXT,
		scope       TEXT NOT NULL DEFAULT 'global',
		session_id  TEXT,
		created_at  INTEGER NOT NULL,
		accessed_at INTEGER,
		access_count INTEGER DEFAULT 0,
		digest      TEXT
	);

	CREATE TABLE IF NOT EXISTS todos (
		id         INTEGER PRIMARY KEY,
		data       TEXT NOT NULL,
		updated_at INTEGER NOT NULL
	);

	-- Armed watches (internal/wake) are durable: a watch that fired while
	-- the process was down is reported as a missed wake on resume. The
	-- table is additive and IF NOT EXISTS, so no schema-version bump is
	-- needed.
	CREATE TABLE IF NOT EXISTS session_watches (
		session_id  TEXT NOT NULL,
		id          TEXT NOT NULL,
		on_spec     TEXT NOT NULL,
		until_spec  TEXT NOT NULL DEFAULT '',
		state       TEXT NOT NULL DEFAULT 'armed',
		evidence    TEXT NOT NULL DEFAULT '',
		armed_at    INTEGER NOT NULL DEFAULT 0,
		expires_at  INTEGER NOT NULL DEFAULT 0,
		fired_at    INTEGER NOT NULL DEFAULT 0,
		interval_ms INTEGER NOT NULL DEFAULT 0,
		owner       TEXT NOT NULL DEFAULT '',
		lease_until INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (session_id, id)
	);

	-- Saved approvals (yaah-x6vi.6, saved_permission_repo.go) are durable,
	-- project-scoped "always" decisions. Additive and IF NOT EXISTS, so no
	-- schema-version bump is needed.
	CREATE TABLE IF NOT EXISTS saved_permissions (
		project_id TEXT NOT NULL,
		action     TEXT NOT NULL,
		resource   TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		UNIQUE (project_id, action, resource)
	);

	-- The read is WHERE session_id = ? ORDER BY armed_at, id. The PK's
	-- (session_id, id) prefix already serves the equality; this index adds
	-- the ordering instead of duplicating the prefix alone.
	DROP INDEX IF EXISTS idx_session_watches_session;
	CREATE INDEX IF NOT EXISTS idx_session_watches_session_armed
		ON session_watches(session_id, armed_at, id);
	`

	if _, err := d.sql.Exec(schema); err != nil {
		return fmt.Errorf("create tables: %w", err)
	}

	ftsMessages := `
	CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
		content, tool_name,
		content='messages', content_rowid='rowid'
	);`
	ftsMemory := `
	CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
		text, tags, source,
		content='memory', content_rowid='rowid'
	);`

	if _, err := d.sql.Exec(ftsMessages); err != nil {
		return fmt.Errorf("create messages_fts: %w", err)
	}
	if _, err := d.sql.Exec(ftsMemory); err != nil {
		return fmt.Errorf("create memory_fts: %w", err)
	}

	// skillLifecycleSchema is the schema v3 addition (skill_repo.go),
	// executed from the version-gated repair transaction below. Every
	// statement is IF NOT EXISTS, so re-running the migration is
	// harmless.
	skillLifecycleSchema := `
	CREATE TABLE IF NOT EXISTS skill_usage (
		name              TEXT PRIMARY KEY,
		use_count         INTEGER NOT NULL DEFAULT 0,
		view_count        INTEGER NOT NULL DEFAULT 0,
		reuse_after_patch INTEGER NOT NULL DEFAULT 0,
		last_used_at      TEXT,
		created_at        TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS skill_provenance (
		name       TEXT PRIMARY KEY,
		created_by TEXT NOT NULL,
		source     TEXT,
		created_at TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS skill_ledger (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		name        TEXT NOT NULL,
		action      TEXT NOT NULL,
		before_hash TEXT,
		after_hash  TEXT,
		delta       TEXT,
		created_at  TEXT NOT NULL
	);
	`

	// triggers seeds a floor version row: databases predating version
	// stamping (or fresh ones) read a concrete number for repair gating
	// instead of nothing. The current schema version is written only
	// inside the repair transaction below — stamping it here, ahead of
	// the repairs, would let a crash between the two make a
	// half-repaired database look current on the next open.
	triggers := `
	INSERT OR IGNORE INTO schema_meta (key, value) VALUES ('version', '1');
	CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
		INSERT INTO messages_fts(rowid, content, tool_name) VALUES (new.rowid, new.content, new.tool_name);
	END;
	CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
		INSERT INTO messages_fts(messages_fts, rowid, content, tool_name) VALUES ('delete', old.rowid, old.content, old.tool_name);
	END;
	CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE ON messages BEGIN
		INSERT INTO messages_fts(messages_fts, rowid, content, tool_name) VALUES ('delete', old.rowid, old.content, old.tool_name);
		INSERT INTO messages_fts(rowid, content, tool_name) VALUES (new.rowid, new.content, new.tool_name);
	END;
	CREATE TRIGGER IF NOT EXISTS memory_ai AFTER INSERT ON memory BEGIN
		INSERT INTO memory_fts(rowid, text, tags, source) VALUES (new.rowid, new.text, new.tags, new.source);
	END;
	CREATE TRIGGER IF NOT EXISTS memory_ad AFTER DELETE ON memory BEGIN
		INSERT INTO memory_fts(memory_fts, rowid, text, tags, source) VALUES ('delete', old.rowid, old.text, old.tags, old.source);
	END;
	CREATE TRIGGER IF NOT EXISTS memory_au AFTER UPDATE ON memory BEGIN
		INSERT INTO memory_fts(memory_fts, rowid, text, tags, source) VALUES ('delete', old.rowid, old.text, old.tags, old.source);
		INSERT INTO memory_fts(rowid, text, tags, source) VALUES (new.rowid, new.text, new.tags, new.source);
	END;
	`

	if _, err := d.sql.Exec(triggers); err != nil {
		return fmt.Errorf("create triggers: %w", err)
	}

	// Column migrations for existing databases. Every step is checked:
	// a failed ALTER must abort startup rather than leave the schema
	// silently missing a column that later queries expect.

	steps := []struct {
		table  string
		column string
		ddl    string
	}{
		{"messages", "tool_call_id", "ALTER TABLE messages ADD COLUMN tool_call_id TEXT"},
		{"messages", "id", "ALTER TABLE messages ADD COLUMN id TEXT DEFAULT ''"},
		{"sessions", "compaction_cooldown_until", "ALTER TABLE sessions ADD COLUMN compaction_cooldown_until INTEGER DEFAULT 0"},
		{"sessions", "ineffective_compactions", "ALTER TABLE sessions ADD COLUMN ineffective_compactions INTEGER DEFAULT 0"},
		{"messages", "reasoning_content", "ALTER TABLE messages ADD COLUMN reasoning_content TEXT DEFAULT ''"},
		{"messages", "reasoning_signature", "ALTER TABLE messages ADD COLUMN reasoning_signature TEXT DEFAULT ''"},
		{"messages", "trace_id", "ALTER TABLE messages ADD COLUMN trace_id TEXT DEFAULT ''"},
		{"messages", "turn_id", "ALTER TABLE messages ADD COLUMN turn_id TEXT DEFAULT ''"},
		{"sessions", "system_prompt", "ALTER TABLE sessions ADD COLUMN system_prompt TEXT DEFAULT ''"},
		{"sessions", "compacted_summary", "ALTER TABLE sessions ADD COLUMN compacted_summary TEXT DEFAULT ''"},
		{"memory", "embedding", "ALTER TABLE memory ADD COLUMN embedding BLOB"},
		{"memory", "digest", "ALTER TABLE memory ADD COLUMN digest TEXT"},
		// Supersession: an atom contradicted or restated by a newer one is
		// marked, never deleted, so the record survives for audit while
		// dropping out of every read path. NULL means live.
		{"memory", "superseded_by", "ALTER TABLE memory ADD COLUMN superseded_by TEXT"},
		// schema v4 (docs/compaction-as-sleep.md §8 P3): the explicit tier
		// column. The DEFAULT backfills existing rows, so the ALTER alone
		// leaves no NULL scope behind.
		{"memory", "scope", "ALTER TABLE memory ADD COLUMN scope TEXT NOT NULL DEFAULT 'global'"},
		// schema v5: the session link a session-scoped atom is pruned by
		// (hygiene.go). Nullable and additive — pre-v5 rows stay NULL and
		// are therefore never reached by PruneSession.
		{"memory", "session_id", "ALTER TABLE memory ADD COLUMN session_id TEXT"},
		{"messages", "embedding", "ALTER TABLE messages ADD COLUMN embedding BLOB"},
		{"sessions", "workspace_backend", "ALTER TABLE sessions ADD COLUMN workspace_backend TEXT DEFAULT ''"},
		{"sessions", "workspace_omitted_tools", "ALTER TABLE sessions ADD COLUMN workspace_omitted_tools TEXT DEFAULT ''"},
		{"sessions", "tokens_cached", "ALTER TABLE sessions ADD COLUMN tokens_cached INTEGER DEFAULT 0"},
		{"sessions", "sub_tokens_in", "ALTER TABLE sessions ADD COLUMN sub_tokens_in INTEGER DEFAULT 0"},
		{"sessions", "sub_tokens_out", "ALTER TABLE sessions ADD COLUMN sub_tokens_out INTEGER DEFAULT 0"},
		{"sessions", "title", "ALTER TABLE sessions ADD COLUMN title TEXT DEFAULT ''"},
		{"sessions", "updated_at", "ALTER TABLE sessions ADD COLUMN updated_at INTEGER DEFAULT 0"},
		// Persisted session mode: additive with a '' default, so existing
		// rows read as build and no backfill (and no schema-version bump)
		// is needed — nothing has to run exactly once.
		{"sessions", "mode", "ALTER TABLE sessions ADD COLUMN mode TEXT DEFAULT ''"},
		// Persisted orchestrator persona (yaah-21uk.9): the resolved
		// persona name a session was created with, so a resume keeps the
		// session's own budget/belt selection after agents.default.agent
		// changes. Additive with a '' default — pre-persona rows read as
		// build at the read sites (SessionPersona, GetSession), the
		// zero-change migration; no backfill and no schema-version bump
		// needed.
		{"sessions", "persona", "ALTER TABLE sessions ADD COLUMN persona TEXT DEFAULT ''"},
		// Watch ownership: a resumed process claims the session's watches,
		// and a fire is delivered only by the owner that claims it.
		{"session_watches", "owner", "ALTER TABLE session_watches ADD COLUMN owner TEXT NOT NULL DEFAULT ''"},
		{"session_watches", "lease_until", "ALTER TABLE session_watches ADD COLUMN lease_until INTEGER NOT NULL DEFAULT 0"},
	}
	for _, s := range steps {
		if err := d.ensureColumn(s.table, s.column, s.ddl); err != nil {
			return err
		}
	}

	// One-time repairs gated on the schema version (a database behind
	// currentSchemaVersion), not on whether an ALTER happened on this
	// open: when the ALTER and its follow-up UPDATE disagree — a crash
	// between them, a transient SQLITE_BUSY on the UPDATE — a
	// column-presence gate would leave rows permanently un-repaired. Here
	// each repair and the version bump commit together: either both
	// happen or the next open retries (self-healing). Future versions
	// append their repair before the bump.
	if dbVersion < currentSchemaVersion {
		tx, err := d.sql.BeginTx(context.Background(), nil) // immediate write lock via DSN
		if err != nil {
			return fmt.Errorf("begin schema repairs: %w", err)
		}
		defer tx.Rollback()
		if dbVersion < activitySchemaVersion {
			// 1 → 2: sessions gained title/updated_at. A session's
			// "last activity" starts as its start time; new writes
			// overwrite updated_at per message.
			if _, err := tx.Exec(
				`UPDATE sessions SET updated_at = started_at WHERE COALESCE(updated_at, 0) = 0`,
			); err != nil {
				return fmt.Errorf("backfill sessions.updated_at: %w", err)
			}
		}
		if dbVersion < skillSchemaVersion {
			// 2 → 3: skill lifecycle tables (docs/compaction-as-sleep.md
			// §8 P1). Purely additive, so the migration *is* the DDL — no
			// backfill to run. It lives here rather than in the base schema
			// so the tables are created atomically with the version bump:
			// a database stamped 3 therefore always has them.
			if _, err := tx.Exec(skillLifecycleSchema); err != nil {
				return fmt.Errorf("create skill lifecycle tables: %w", err)
			}
		}
		if dbVersion < memorySchemaVersion {
			// 3 → 4: memory gained the explicit scope tier
			// (docs/compaction-as-sleep.md §8 P3, deep sleep). The ALTER
			// added the column with a 'global' default; this repairs any
			// row a pre-v4 writer left empty, so a database stamped 4
			// always carries a concrete scope per row.
			if _, err := tx.Exec(
				`UPDATE memory SET scope = 'global' WHERE COALESCE(scope, '') = ''`,
			); err != nil {
				return fmt.Errorf("backfill memory.scope: %w", err)
			}
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_meta (key, value) VALUES ('version', ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			strconv.Itoa(currentSchemaVersion),
		); err != nil {
			return fmt.Errorf("bump schema version: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit schema repairs: %w", err)
		}
	}

	// Index on the turn cross-link must be created after the turn_id
	// column migration above — on a legacy database the column does not
	// exist while the base schema still executes.
	if _, err := d.sql.Exec("CREATE INDEX IF NOT EXISTS idx_messages_turn ON messages(turn_id)"); err != nil {
		return fmt.Errorf("create idx_messages_turn: %w", err)
	}

	// Backfill digests for memory rows created before the digest migration.
	if _, err := d.ReconcileMemoryDigests(); err != nil {
		return fmt.Errorf("reconcile memory digests: %w", err)
	}

	return nil
}

// ensureColumn adds column to table via ddl when the column is missing,
// returning any error from the pragma lookup or the ALTER. Migrations
// must never silently no-op: a schema missing a column fails later
// queries with confusing errors far from the root cause.
func (d *DB) ensureColumn(table, column, ddl string) error {
	var has int
	err := d.sql.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", table, column,
	).Scan(&has)
	if err != nil {
		return fmt.Errorf("check %s.%s: %w", table, column, err)
	}
	if has == 1 {
		return nil
	}
	if _, err := d.sql.Exec(ddl); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}
