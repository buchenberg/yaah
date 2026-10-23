// Package memory provides the SQLite-backed persistence layer for yaah.
// It manages three concerns over a single database file:
//
//   - Sessions: conversation sessions and their messages (session_repo.go,
//     message_repo.go)
//   - Long-term memory: searchable notes with FTS5 full-text indexing
//     (memory.go, repository.go)
//   - Todo persistence: saving/loading the in-memory todo store (todo.go)
//
// The DB type is the single entry point for all database operations.
package memory

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// currentSchemaVersion is the schema version this binary writes and
// understands. A database stamped with a higher version was created by
// a newer yaah and is refused at open (review A6). Stored in
// schema_meta as a decimal string; compared numerically.
//
// Repairs that must run exactly once (backfills over existing rows) are
// keyed to this version, not to whether an ALTER ran on the same open:
// migrate commits each repair and its version bump together, so a
// transient failure re-runs the pair on the next open instead of
// skipping a half-migrated database forever.
const currentSchemaVersion = 5

// activitySchemaVersion is the schema version at which
// sessions.updated_at came into being (its values were seeded from
// started_at at that transition). Repairs gated with `dbVersion <
// activitySchemaVersion` run inside those parameters.
const activitySchemaVersion = 2

// skillSchemaVersion is the schema version that introduced the skill
// lifecycle tables (skill_usage, skill_provenance, skill_ledger). The
// transition is purely additive: the migration runs the DDL once inside the
// version-gated repair transaction (migrate) and then stamps the version —
// no data backfill is required. See skill_repo.go.
const skillSchemaVersion = 3

// memorySchemaVersion is the schema version that introduced memory.scope, the
// explicit tier column (docs/compaction-as-sleep.md §8 P3): 'session' for
// atoms distilled from the running conversation, 'global' for entries meant to
// outlive it. The transition is additive — the ALTER runs from the
// column-migration list and the version-gated repair backfills any row the
// column left empty — so a database stamped 4 always carries a concrete scope
// per row.
const memorySchemaVersion = 4

// maxSessionPromptBytes caps the persisted system_prompt and
// compacted_summary columns so unbounded prompts cannot bloat the DB
// (review A6 / S7). Overlong content is truncated with a marker.
const maxSessionPromptBytes = 64 * 1024

// truncateWithMarker caps s at max bytes, replacing any partial UTF-8
// sequence at the cut point and appending a truncation marker when cut.
func truncateWithMarker(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := strings.ToValidUTF8(s[:max], "")
	return cut + "\n…[truncated]"
}

// Memory scopes are the explicit tiers of docs/compaction-as-sleep.md §8 P3
// (directions 4/5): ScopeSession marks an atom distilled from the running
// conversation — the consolidator's "deep sleep" product — and ScopeGlobal
// marks one meant to outlive any single session. An empty scope means
// ScopeGlobal.
const (
	ScopeGlobal  = "global"
	ScopeSession = "session"

	// ScopeAll is a read-path-only widening: it asks a search to span every
	// tier. It is deliberately not a persistable tier — NormalizeScope
	// rejects it — so no row can be written with scope = "all" and become
	// invisible to a scope = ? filter.
	ScopeAll = "all"
)

// NormalizeScope maps the empty scope to ScopeGlobal and lowercases the known
// tiers. An unknown value is an error: persisting an unfilterable tier would
// make the row invisible to every scope = ? query, so it must fail loudly
// instead of being written verbatim.
func NormalizeScope(scope string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "", ScopeGlobal:
		return ScopeGlobal, nil
	case ScopeSession:
		return ScopeSession, nil
	default:
		return "", fmt.Errorf("unknown memory scope %q (want %q or %q)", scope, ScopeSession, ScopeGlobal)
	}
}

// NormalizeScopeFilter validates the tier filter of a read path. Unlike
// NormalizeScope it accepts ScopeAll, because widening a query cannot make a
// row unreachable the way writing an unknown tier can.
//
// It deliberately does not treat the empty string as "every tier". The store
// used to read "" as "no predicate" while the tool layer read the same "" as
// "global only", so one input meant opposite things a hundred lines apart.
// Every read now has to name its tier.
func NormalizeScopeFilter(scope string) (string, error) {
	switch s := strings.ToLower(strings.TrimSpace(scope)); s {
	case ScopeGlobal, ScopeSession, ScopeAll:
		return s, nil
	case "":
		return "", fmt.Errorf("memory scope filter is required: name %q, %q, or %q", ScopeGlobal, ScopeSession, ScopeAll)
	default:
		return "", fmt.Errorf("unknown memory scope %q (want %q, %q, or %q)", scope, ScopeGlobal, ScopeSession, ScopeAll)
	}
}

// Entry represents a long-term memory note. Scope is its tier (ScopeSession
// or ScopeGlobal); it is always populated on read. SessionID links a
// session-scoped atom to the conversation that distilled it and is empty on
// global atoms and on every row written before schema v5 — the garbage
// collector in hygiene.go needs a non-empty value before it touches a row.
type Entry struct {
	ID          string `json:"id"`
	Text        string `json:"text"`
	Tags        string `json:"tags"` // JSON array
	Source      string `json:"source"`
	Scope       string `json:"scope"`
	SessionID   string `json:"session_id,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	AccessedAt  int64  `json:"accessed_at,omitempty"`
	AccessCount int    `json:"access_count"`
}

// Session represents a conversation session.
type Session struct {
	ID        string `json:"id"`
	StartedAt int64  `json:"started_at"`
	EndedAt   int64  `json:"ended_at,omitempty"`
	CWD       string `json:"cwd"`
	Model     string `json:"model"`
	TokensIn  int    `json:"tokens_in"`
	TokensOut int    `json:"tokens_out"`
	// TokensCached is the sum of prompt tokens the provider served
	// from its context cache (deepseek automatic prefix cache). A
	// session killed mid-run still reports its spend because usage
	// flushes incrementally (UpdateSessionUsage), not only at EndSession.
	TokensCached int `json:"tokens_cached,omitempty"`
	// SubTokensIn/SubTokensOut attribute the sub-agent share of the
	// session total (blocking dispatches via the loop fold, background
	// dispatches via the jobs manager). The totals include sub-agent
	// spend; orchestrator spend = total - sub.
	SubTokensIn      int    `json:"sub_tokens_in,omitempty"`
	SubTokensOut     int    `json:"sub_tokens_out,omitempty"`
	SystemPrompt     string `json:"system_prompt,omitempty"`
	CompactedSummary string `json:"compacted_summary,omitempty"`
	// WorkspaceBackend records the workspace substrate the session ran
	// under ("local" or "containerd"), and WorkspaceOmittedTools the
	// host-only tool names isolated mode left out (comma-separated).
	// Decision D4 of the isolated-workspace plan: a run's available tool
	// set differs by mode, so "the model didn't use background_process"
	// and "the model couldn't" must stay distinguishable in the record.
	WorkspaceBackend      string `json:"workspace_backend,omitempty"`
	WorkspaceOmittedTools string `json:"workspace_omitted_tools,omitempty"`
	// Title is a short human-readable label derived from the first user
	// message (session pickers, `yaah sessions`, ACP session/list).
	Title string `json:"title,omitempty"`
	// UpdatedAt is the ts of the last persisted message; 0 on a session
	// with no messages yet (list queries COALESCE it to started_at).
	UpdatedAt int64 `json:"updated_at,omitempty"`
}

// Message represents a single message in a session.
type Message struct {
	ID                 string `json:"id"`
	SessionID          string `json:"session_id"`
	Idx                int    `json:"idx"`
	Role               string `json:"role"`
	Content            string `json:"content"`
	ReasoningContent   string `json:"reasoning_content,omitempty"`
	ReasoningSignature string `json:"reasoning_signature,omitempty"`
	ToolName           string `json:"tool_name,omitempty"`
	ToolCallID         string `json:"tool_call_id,omitempty"`
	ToolCalls          string `json:"tool_calls,omitempty"`
	Timestamp          int64  `json:"ts"`
	TraceID            string `json:"trace_id,omitempty"`
	TurnID             string `json:"turn_id,omitempty"`
}

// DB is the yaah persistent database.
type DB struct {
	sql *sql.DB
	// embedder is optional; set via SetEmbedder. embMu guards the field
	// only — it must never be held across SQL/exec calls or embedder
	// network calls. Take a snapshot via Embedder() and use that.
	embedder  Embedder
	embMu     sync.Mutex
	embedWarn embedWarnLimiter
	// embeds tracks in-flight background embedding writes so Close can
	// drain them before the SQL pool is closed.
	embeds pendingEmbeds
}

// SetEmbedder configures the embedding model used for semantic search.
// When nil, vector search is disabled. Safe for concurrent use.
func (d *DB) SetEmbedder(e Embedder) {
	d.embMu.Lock()
	defer d.embMu.Unlock()
	d.embedder = e
}

// Embedder returns the configured embedding model, or nil.
// Safe for concurrent use.
func (d *DB) Embedder() Embedder {
	d.embMu.Lock()
	defer d.embMu.Unlock()
	return d.embedder
}

// Open opens (or creates) the yaah database at the given path.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	d := &DB{sql: db}
	if err := d.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return d, nil
}

// Close drains in-flight background embedding writes (bounded by
// embedDrainTimeout) and then closes the database connection. Draining first
// keeps a short-lived process from racing its own embeds against the pool
// shutdown.
func (d *DB) Close() error {
	d.embeds.drain(embedDrainTimeout)
	return d.sql.Close()
}

// sqliteDSN appends per-connection pragmas to the SQLite DSN. The
// modernc.org/sqlite driver applies `_pragma` query parameters to every new
// pooled connection (busy_timeout first), unlike a one-off `PRAGMA` Exec
// which only affects a single connection. Existing query parameters are
// preserved. `_txlock=immediate` makes Begin() take the write lock up front,
// so the AddMemoryDedup lookup+insert transaction is atomic.
func sqliteDSN(path string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate"
}

// sanitizeFTSQuery escapes special FTS5 characters and wraps each word
// in quotes for exact matching.
func sanitizeFTSQuery(query string) string {
	words := strings.Fields(query)
	var quoted []string
	for _, w := range words {
		w = strings.ReplaceAll(w, `"`, `""`)
		quoted = append(quoted, `"`+w+`"`)
	}
	return strings.Join(quoted, " ")
}

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
		updated_at      INTEGER DEFAULT 0
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

// memoryDigest returns the exact content digest for a memory entry's text.
// Used for exact dedup; a plain SHA-256 suffices because memory text is a
// plain string (no need for shepherd's canonical-JSON key sorting).
func memoryDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// NewEntryID returns a fresh memory entry ID. IDs were previously
// mem-<UnixNano>, which collides when two entries are created within the
// clock's nanosecond resolution — coarse clocks make that a primary-key
// collision for back-to-back inserts, not a theoretical one. The random form
// matches newTurnID's shape: 128 bits of crypto/rand, hex-encoded, with a
// time-based fallback only if the system entropy source is unavailable.
func NewEntryID() string {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		return fmt.Sprintf("mem-%d", time.Now().UnixNano())
	}
	return "mem-" + hex.EncodeToString(b[:])
}

// AddMemory inserts a new memory entry. The caller should separately call
// EmbedMemoryAsync to produce the embedding vector.
func (d *DB) AddMemory(e Entry) error {
	scope, err := NormalizeScope(e.Scope)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(
		`INSERT INTO memory (id, text, tags, source, scope, session_id, created_at, digest) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Text, e.Tags, e.Source, scope, nullableText(e.SessionID), e.CreatedAt, memoryDigest(e.Text),
	)
	return err
}

// nullableText maps the empty string to SQL NULL: a global atom (or any atom
// written by a caller with no session) carries no session link, and the
// pruning sweeps in hygiene.go must never match an empty string as if it
// named a session.
func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// EmbedMemoryAsync embeds the text in a background goroutine and stores
// the result. Returns a channel that closes when the embed completes —
// or immediately, with nothing written, when no embedder is configured
// or the store is closing (a skipped embed is recovered by the reconcile
// pass). Callers that need to wait for the embedding (e.g. the
// memory_add tool) can read from the channel.
func (d *DB) EmbedMemoryAsync(id, text string) <-chan struct{} {
	embedder := d.Embedder()
	if embedder == nil {
		return nil
	}
	done := make(chan struct{})
	// Drain cancels the context, so a slow embedding server cannot keep
	// the write queued past a timed-out Close.
	embedCtx, cancel := context.WithCancel(context.Background())
	finish, ok := d.embeds.begin(cancel)
	if !ok {
		// Close has begun: skip the write rather than race the pool.
		cancel()
		close(done)
		return done
	}
	go func() {
		defer finish()
		defer close(done)
		defer cancel()
		emb, err := embedder.Embed(embedCtx, text)
		if err != nil {
			d.recordEmbedFailure("embed_memory", err)
			return
		}
		if _, err := d.sql.Exec(`UPDATE memory SET embedding = ? WHERE id = ?`, EncodeEmbedding(emb), id); err != nil {
			slog.Warn("memory: store embedding failed", "op", "embed_memory", "id", id, "err", err)
		}
	}()
	return done
}

// AddMemoryDedup adds a memory entry, skipping if text is identical to an
// existing entry. Returns the ID of the duplicate if found, or empty string
// if the entry was added. The lookup and insert run in a single write
// transaction (the DSN sets _txlock=immediate) so concurrent dedup calls
// cannot both observe "no match" and insert duplicate rows.
func (d *DB) AddMemoryDedup(e Entry) (string, error) {
	digest := memoryDigest(e.Text)

	tx, err := d.sql.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var dupID string
	err = tx.QueryRow(`SELECT id FROM memory WHERE digest = ? AND superseded_by IS NULL LIMIT 1`, digest).Scan(&dupID)
	if err == nil {
		return dupID, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}

	scope, err := NormalizeScope(e.Scope)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(
		`INSERT INTO memory (id, text, tags, source, scope, session_id, created_at, digest) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Text, e.Tags, e.Source, scope, nullableText(e.SessionID), e.CreatedAt, digest,
	); err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return "", nil
}

// SearchMemory searches memory entries using FTS5 and bumps access counters
// for returned results. An optional tag can be passed to filter by tag.
func (d *DB) SearchMemory(query string, limit int, tag ...string) ([]Entry, error) {
	tagFilter := ""
	if len(tag) > 0 {
		tagFilter = tag[0]
	}
	return d.SearchMemoryScoped(query, limit, tagFilter, ScopeAll)
}

// SearchMemoryScoped is SearchMemory with an explicit scope (tier) filter.
// scope must name a tier: ScopeGlobal, ScopeSession, or ScopeAll. Each returned
// entry carries its own Scope.
func (d *DB) SearchMemoryScoped(query string, limit int, tagFilter, scope string) ([]Entry, error) {
	scope, err := NormalizeScopeFilter(scope)
	if err != nil {
		return nil, err
	}

	const (
		cols     = "id, text, tags, source, scope, created_at, COALESCE(accessed_at, 0), access_count"
		colsJoin = "m.id, m.text, m.tags, m.source, m.scope, m.created_at, COALESCE(m.accessed_at, 0), m.access_count"
	)

	// tags/source also exist on the FTS table, so the join branch's
	// predicates are qualified with the row alias; the plain branch needs
	// no alias.
	qual := ""
	if query != "" {
		qual = "m."
	}
	where := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if tagFilter != "" {
		where = append(where, qual+"tags LIKE ?")
		args = append(args, `%"`+strings.ReplaceAll(tagFilter, `"`, `""`)+`"%`)
	}
	if scope != ScopeAll {
		where = append(where, qual+"scope = ?")
		args = append(args, scope)
	}
	// A superseded atom is history: kept for audit, never surfaced, so a
	// corrected fact cannot come back from the dead on the next recall.
	where = append(where, qual+"superseded_by IS NULL")

	var querySQL string
	if query == "" {
		querySQL = `SELECT ` + cols + ` FROM memory`
		if len(where) > 0 {
			querySQL += ` WHERE ` + strings.Join(where, " AND ")
		}
		querySQL += ` ORDER BY created_at DESC LIMIT ?`
	} else {
		where = append([]string{"memory_fts MATCH ?"}, where...)
		args = append([]any{sanitizeFTSQuery(query)}, args...)
		querySQL = `SELECT ` + colsJoin + `
			FROM memory m
			JOIN memory_fts ON memory_fts.rowid = m.rowid
			WHERE ` + strings.Join(where, " AND ") + `
			ORDER BY rank
			LIMIT ?`
	}
	args = append(args, limit)

	rows, err := d.sql.Query(querySQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Entry
	now := time.Now().Unix()
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Text, &e.Tags, &e.Source, &e.Scope, &e.CreatedAt, &e.AccessedAt, &e.AccessCount); err != nil {
			return nil, err
		}
		results = append(results, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range results {
		results[i].AccessCount++
		results[i].AccessedAt = now
	}
	ids := make([]string, len(results))
	for i, e := range results {
		ids[i] = e.ID
	}
	d.bumpAccessCountIDs(ids, now)

	return results, nil
}

// VectorResult is a memory entry with its cosine similarity score.
type VectorResult struct {
	Entry
	Score float32
}

// SearchMemoryVector embeds the query, scans all rows with non-null
// embedding, and returns the top-K sorted by cosine similarity
// (descending). Falls back to an empty result when no embedder is
// configured or the embedding call fails; a failed embedding call is
// surfaced via the rate-limited warning log and the OTel embed-failure
// counter.
func (d *DB) SearchMemoryVector(ctx context.Context, query string, limit int) ([]VectorResult, error) {
	return d.SearchMemoryVectorScoped(ctx, query, limit, ScopeAll)
}

// SearchMemoryVectorScoped is SearchMemoryVector with an explicit scope (tier)
// filter. scope must name a tier: ScopeGlobal, ScopeSession, or ScopeAll.
func (d *DB) SearchMemoryVectorScoped(ctx context.Context, query string, limit int, scope string) ([]VectorResult, error) {
	scope, err := NormalizeScopeFilter(scope)
	if err != nil {
		return nil, err
	}
	embedder := d.Embedder()
	if embedder == nil {
		return nil, nil
	}
	qEmb, err := embedder.Embed(ctx, query)
	if err != nil {
		d.recordEmbedFailure("search_memory_vector", err)
		return nil, nil
	}

	vecSQL := `
		SELECT id, text, tags, source, scope, created_at, COALESCE(accessed_at, 0), access_count, COALESCE(embedding, '')
		FROM memory
		WHERE embedding IS NOT NULL AND superseded_by IS NULL`
	var vecArgs []any
	if scope != ScopeAll {
		vecSQL += ` AND scope = ?`
		vecArgs = append(vecArgs, scope)
	}
	rows, err := d.sql.Query(vecSQL, vecArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type candidate struct {
		e    Entry
		blob []byte
	}
	var candidates []candidate
	for rows.Next() {
		var e Entry
		var blob []byte
		if err := rows.Scan(&e.ID, &e.Text, &e.Tags, &e.Source, &e.Scope, &e.CreatedAt, &e.AccessedAt, &e.AccessCount, &blob); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate{e, blob})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var results []VectorResult
	for _, c := range candidates {
		if len(c.blob) == 0 {
			continue
		}
		emb := DecodeEmbedding(c.blob)
		score := CosineSimilarity(qEmb, emb)
		results = append(results, VectorResult{Entry: c.e, Score: score})
	}

	// Sort descending by score (sort.Slice — the hand-rolled bubble
	// sort was O(n²) on every vector search, review A6).
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	now := time.Now().Unix()
	ids := make([]string, len(results))
	for i := range results {
		results[i].AccessCount++
		results[i].AccessedAt = now
		ids[i] = results[i].ID
	}
	d.bumpAccessCountIDs(ids, now)

	return results, nil
}

// bumpAccessCountIDs records one access for every id in a single
// statement — the previous per-row UPDATE loop issued N round-trips
// per search (review A6).
func (d *DB) bumpAccessCountIDs(ids []string, now int64) {
	if len(ids) == 0 {
		return
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, now)
	for _, id := range ids {
		args = append(args, id)
	}
	if _, err := d.sql.Exec(
		`UPDATE memory SET access_count = access_count + 1, accessed_at = ? WHERE id IN (`+placeholders+`)`,
		args...); err != nil {
		// Best-effort bookkeeping: a failure only means access stats
		// drift, so the search result is still returned — but the drift
		// must not be silent.
		slog.Warn("memory: access-count bump failed", "ids", len(ids), "err", err)
	}
}

// ReconcileEmbeddings finds memory rows with NULL embedding, embeds their
// text using the configured embedder, and stores the results. It returns
// the number of rows updated. An error from any single embed does not stop
// the reconciliation; each failure is surfaced via the rate-limited
// warning log and the OTel embed-failure counter.
func (d *DB) ReconcileEmbeddings(ctx context.Context) (int, error) {
	embedder := d.Embedder()
	if embedder == nil {
		return 0, nil
	}
	rows, err := d.sql.Query(`SELECT id, text FROM memory WHERE embedding IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("reconcile: query: %w", err)
	}
	defer rows.Close()

	type row struct {
		id, text string
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.text); err != nil {
			return 0, err
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	updated := 0
	for _, r := range pending {
		emb, err := embedder.Embed(ctx, r.text)
		if err != nil {
			d.recordEmbedFailure("reconcile_embeddings", err)
			continue
		}
		if _, err := d.sql.Exec(`UPDATE memory SET embedding = ? WHERE id = ?`, EncodeEmbedding(emb), r.id); err != nil {
			slog.Warn("memory: store embedding failed", "op", "reconcile_embeddings", "id", r.id, "err", err)
			continue
		}
		updated++
	}
	return updated, nil
}

// GetMemory retrieves a single memory entry by ID.
func (d *DB) GetMemory(id string) (Entry, error) {
	row := d.sql.QueryRow(`
		SELECT id, text, tags, source, scope, COALESCE(session_id, ''), created_at, COALESCE(accessed_at, 0), access_count
		FROM memory WHERE id = ?
	`, id)
	var e Entry
	err := row.Scan(&e.ID, &e.Text, &e.Tags, &e.Source, &e.Scope, &e.SessionID, &e.CreatedAt, &e.AccessedAt, &e.AccessCount)
	return e, err
}

// ErrMemoryNotFound is the sentinel wrapped (with the offending ID) by
// DeleteMemory and UpdateMemory when no row matches, so consumers can
// separate a missing ID from a genuine database failure with errors.Is
// instead of substring-matching a message (#330 review follow-up).
var ErrMemoryNotFound = errors.New("memory not found")

// DeleteMemory removes a memory entry by ID. A delete matching no rows
// is an error, not a success: tool callers pass back whatever ID the
// tools surfaced, and a silent no-op delete let truncated IDs report
// "deleted" while the row — and its duplicates — accumulated (#328).
func (d *DB) DeleteMemory(id string) error {
	result, err := d.sql.Exec(`DELETE FROM memory WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrMemoryNotFound, id)
	}
	return nil
}

// SupersedeMemory marks oldID as superseded by newID. The row is kept for
// audit but excluded from every read path, so a contradicted fact stops
// surfacing instead of being silently retained beside its correction. A second
// supersede of the same atom is an error: the first correction is the one that
// counts, and silently re-pointing it would lose that provenance.
func (d *DB) SupersedeMemory(oldID, newID string) error {
	if oldID == newID {
		return fmt.Errorf("memory %s cannot supersede itself", oldID)
	}
	result, err := d.sql.Exec(
		`UPDATE memory SET superseded_by = ? WHERE id = ? AND superseded_by IS NULL`,
		newID, oldID,
	)
	if err != nil {
		return fmt.Errorf("supersede %s: %w", oldID, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: %s (missing or already superseded)", ErrMemoryNotFound, oldID)
	}
	return nil
}

// SupersededBy reports which atom superseded id, or "" when id is still live.
func (d *DB) SupersededBy(id string) (string, error) {
	var by sql.NullString
	if err := d.sql.QueryRow(`SELECT superseded_by FROM memory WHERE id = ?`, id).Scan(&by); err != nil {
		return "", err
	}
	return by.String, nil
}

// PromoteMemory moves an atom from the session tier into the global one and
// returns the tier it came from. Promotion is one-way on purpose: a global fact
// is a claim about the world, and a demoted atom would only be re-promoted (and
// re-contested) on the next recall. Promoting an atom that is already global is
// a no-op rather than an error, so a retry after a partial failure is safe.
func (d *DB) PromoteMemory(id string) (string, error) {
	var scope, supersededBy sql.NullString
	err := d.sql.QueryRow(`SELECT scope, superseded_by FROM memory WHERE id = ?`, id).Scan(&scope, &supersededBy)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %s", ErrMemoryNotFound, id)
	}
	if err != nil {
		return "", err
	}
	if supersededBy.Valid {
		return "", fmt.Errorf("memory %s is superseded by %s; promote the replacement instead", id, supersededBy.String)
	}
	if scope.String == ScopeGlobal {
		return scope.String, nil
	}
	if _, err := d.sql.Exec(`UPDATE memory SET scope = ? WHERE id = ?`, ScopeGlobal, id); err != nil {
		return "", fmt.Errorf("promote %s: %w", id, err)
	}
	return scope.String, nil
}

// UpdateMemory updates the text of an existing memory entry.
func (d *DB) UpdateMemory(id string, text string) error {
	result, err := d.sql.Exec(`UPDATE memory SET text = ?, digest = ? WHERE id = ?`, text, memoryDigest(text), id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrMemoryNotFound, id)
	}
	return nil
}

// ReconcileMemoryDigests backfills the digest column for rows where it is
// NULL (e.g. entries added before the digest migration). Returns the number
// of rows updated.
func (d *DB) ReconcileMemoryDigests() (int, error) {
	rows, err := d.sql.Query(`SELECT id, text FROM memory WHERE digest IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("reconcile digests: query: %w", err)
	}
	defer rows.Close()

	type row struct {
		id, text string
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.text); err != nil {
			return 0, err
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	updated := 0
	for _, r := range pending {
		if _, err := d.sql.Exec(`UPDATE memory SET digest = ? WHERE id = ?`, memoryDigest(r.text), r.id); err != nil {
			return updated, fmt.Errorf("reconcile digests: update %s: %w", r.id, err)
		}
		updated++
	}
	return updated, nil
}

// ListMemory returns the most recent memory entries.
func (d *DB) ListMemory(limit int) ([]Entry, error) {
	return d.ListMemoryScoped(limit, ScopeAll)
}

// CountMemories reports how many entries live in the named tier: ScopeGlobal,
// ScopeSession, or ScopeAll. It exists so a caller can say "there are memories
// here, just not in the tier you searched" instead of reporting a bare miss.
func (d *DB) CountMemories(scope string) (int, error) {
	scope, err := NormalizeScopeFilter(scope)
	if err != nil {
		return 0, err
	}
	countSQL := `SELECT COUNT(*) FROM memory WHERE superseded_by IS NULL`
	var args []any
	if scope != ScopeAll {
		countSQL += ` AND scope = ?`
		args = append(args, scope)
	}
	var n int
	if err := d.sql.QueryRow(countSQL, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count memory: %w", err)
	}
	return n, nil
}

// ListMemoryScoped returns the most recent memory entries, restricted to the
// named tier (ScopeGlobal, ScopeSession, or ScopeAll).
func (d *DB) ListMemoryScoped(limit int, scope string) ([]Entry, error) {
	scope, err := NormalizeScopeFilter(scope)
	if err != nil {
		return nil, err
	}

	listSQL := `
		SELECT id, text, tags, source, scope, COALESCE(session_id, ''), created_at, COALESCE(accessed_at, 0), access_count
		FROM memory
		WHERE superseded_by IS NULL`
	var listArgs []any
	if scope != ScopeAll {
		listSQL += ` AND scope = ?`
		listArgs = append(listArgs, scope)
	}
	listSQL += ` ORDER BY created_at DESC LIMIT ?`
	listArgs = append(listArgs, limit)

	rows, err := d.sql.Query(listSQL, listArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Text, &e.Tags, &e.Source, &e.Scope, &e.SessionID, &e.CreatedAt, &e.AccessedAt, &e.AccessCount); err != nil {
			return nil, err
		}
		results = append(results, e)
	}
	return results, rows.Err()
}

// DefaultPath returns the default database path (~/.yaah/state.db).
func DefaultPath() string {
	home := os.Getenv("YAAH_HOME")
	if home == "" {
		userHome, _ := os.UserHomeDir()
		home = filepath.Join(userHome, ".yaah")
	}
	return filepath.Join(home, "state.db")
}

// OpenDefault opens the database at the default path.
func OpenDefault() (*DB, error) {
	return Open(DefaultPath())
}
