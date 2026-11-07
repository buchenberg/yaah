// Package memory provides the SQLite-backed persistence layer for yaah.
// It manages three concerns over a single database file:
//
//   - Sessions: conversation sessions and their messages (session_repo.go,
//     message_repo.go)
//   - Long-term memory: searchable notes with FTS5 full-text indexing
//     (memory_crud.go, memory_search.go, memory_vector.go,
//     memory_lifecycle.go, repository.go)
//   - Todo persistence: saving/loading the in-memory todo store (todo.go)
//
// The DB type is the single entry point for all database operations.
package memory

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

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
	// dimWarn rate-limits the stale-dimensionality warning (yaah-shq): a
	// store left behind by a previous embedding model warns once per width
	// per interval instead of on every search, because the condition repeats
	// until the rows are re-embedded.
	dimWarn dimWarnLimiter
	// embeds tracks in-flight background embedding writes so Close can
	// drain them before the SQL pool is closed.
	embeds pendingEmbeds
	// vecMu serializes the vec0 virtual-table DDL (CREATE/DROP), which is a
	// shared write reached by concurrent embed goroutines.
	vecMu sync.Mutex
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
