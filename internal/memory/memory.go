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
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
const currentSchemaVersion = 1

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

// Entry represents a long-term memory note.
type Entry struct {
	ID          string `json:"id"`
	Text        string `json:"text"`
	Tags        string `json:"tags"` // JSON array
	Source      string `json:"source"`
	CreatedAt   int64  `json:"created_at"`
	AccessedAt  int64  `json:"accessed_at,omitempty"`
	AccessCount int    `json:"access_count"`
}

// Session represents a conversation session.
type Session struct {
	ID               string `json:"id"`
	StartedAt        int64  `json:"started_at"`
	EndedAt          int64  `json:"ended_at,omitempty"`
	CWD              string `json:"cwd"`
	Model            string `json:"model"`
	TokensIn         int    `json:"tokens_in"`
	TokensOut        int    `json:"tokens_out"`
	SystemPrompt     string `json:"system_prompt,omitempty"`
	CompactedSummary string `json:"compacted_summary,omitempty"`
}

// Message represents a single message in a session.
type Message struct {
	ID               string `json:"id"`
	SessionID        string `json:"session_id"`
	Idx              int    `json:"idx"`
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	ToolName         string `json:"tool_name,omitempty"`
	ToolCallID       string `json:"tool_call_id,omitempty"`
	ToolCalls        string `json:"tool_calls,omitempty"`
	Timestamp        int64  `json:"ts"`
	TraceID          string `json:"trace_id,omitempty"`
	TurnID           string `json:"turn_id,omitempty"`
}

// DB is the yaah persistent database.
type DB struct {
	sql      *sql.DB
	embedder Embedder // optional; set via SetEmbedder
	embMu    sync.Mutex
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

// Close closes the database connection.
func (d *DB) Close() error {
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
	switch {
	case err == nil:
		// Numeric comparison — lexicographic ordering breaks at
		// multi-digit versions ("10" < "2" as strings).
		dbVersion, convErr := strconv.Atoi(version)
		if convErr != nil {
			return fmt.Errorf("invalid memory db schema version %q", version)
		}
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
		tokens_out INTEGER DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS messages (
		session_id        TEXT NOT NULL REFERENCES sessions(id),
		idx               INTEGER NOT NULL,
		role              TEXT NOT NULL,
		content           TEXT NOT NULL,
		reasoning_content TEXT DEFAULT '',
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
		{"messages", "trace_id", "ALTER TABLE messages ADD COLUMN trace_id TEXT DEFAULT ''"},
		{"messages", "turn_id", "ALTER TABLE messages ADD COLUMN turn_id TEXT DEFAULT ''"},
		{"sessions", "system_prompt", "ALTER TABLE sessions ADD COLUMN system_prompt TEXT DEFAULT ''"},
		{"sessions", "compacted_summary", "ALTER TABLE sessions ADD COLUMN compacted_summary TEXT DEFAULT ''"},
		{"memory", "embedding", "ALTER TABLE memory ADD COLUMN embedding BLOB"},
		{"memory", "digest", "ALTER TABLE memory ADD COLUMN digest TEXT"},
		{"messages", "embedding", "ALTER TABLE messages ADD COLUMN embedding BLOB"},
	}
	for _, s := range steps {
		if err := d.ensureColumn(s.table, s.column, s.ddl); err != nil {
			return err
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

// AddMemory inserts a new memory entry. The caller should separately call
// EmbedMemoryAsync to produce the embedding vector.
func (d *DB) AddMemory(e Entry) error {
	_, err := d.sql.Exec(
		`INSERT INTO memory (id, text, tags, source, created_at, digest) VALUES (?, ?, ?, ?, ?, ?)`,
		e.ID, e.Text, e.Tags, e.Source, e.CreatedAt, memoryDigest(e.Text),
	)
	return err
}

// EmbedMemoryAsync embeds the text in a background goroutine and stores
// the result. Returns a channel that closes when the embed completes, or
// nil when no embedder is configured. Callers that need to wait for the
// embedding (e.g. the memory_add tool) can read from the channel.
func (d *DB) EmbedMemoryAsync(id, text string) <-chan struct{} {
	if d.embedder == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		emb, err := d.embedder.Embed(context.Background(), text)
		if err != nil {
			return
		}
		d.embMu.Lock()
		d.sql.Exec(`UPDATE memory SET embedding = ? WHERE id = ?`, EncodeEmbedding(emb), id)
		d.embMu.Unlock()
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
	err = tx.QueryRow(`SELECT id FROM memory WHERE digest = ? LIMIT 1`, digest).Scan(&dupID)
	if err == nil {
		return dupID, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}

	if _, err := tx.Exec(
		`INSERT INTO memory (id, text, tags, source, created_at, digest) VALUES (?, ?, ?, ?, ?, ?)`,
		e.ID, e.Text, e.Tags, e.Source, e.CreatedAt, digest,
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

	var rows *sql.Rows
	var err error

	if query == "" && tagFilter == "" {
		rows, err = d.sql.Query(`
			SELECT id, text, tags, source, created_at, COALESCE(accessed_at, 0), access_count
			FROM memory
			ORDER BY created_at DESC
			LIMIT ?
		`, limit)
	} else if tagFilter != "" {
		safeTag := `%"` + strings.ReplaceAll(tagFilter, `"`, `""`) + `"%`
		if query == "" {
			rows, err = d.sql.Query(`
				SELECT id, text, tags, source, created_at, COALESCE(accessed_at, 0), access_count
				FROM memory
				WHERE tags LIKE ?
				ORDER BY created_at DESC
				LIMIT ?
			`, safeTag, limit)
		} else {
			safeQuery := sanitizeFTSQuery(query)
			rows, err = d.sql.Query(`
				SELECT m.id, m.text, m.tags, m.source, m.created_at, COALESCE(m.accessed_at, 0), m.access_count
				FROM memory m
				JOIN memory_fts ON memory_fts.rowid = m.rowid
				WHERE memory_fts MATCH ? AND m.tags LIKE ?
				ORDER BY rank
				LIMIT ?
			`, safeQuery, safeTag, limit)
		}
	} else {
		safeQuery := sanitizeFTSQuery(query)
		rows, err = d.sql.Query(`
			SELECT m.id, m.text, m.tags, m.source, m.created_at, COALESCE(m.accessed_at, 0), m.access_count
			FROM memory m
			JOIN memory_fts ON memory_fts.rowid = m.rowid
			WHERE memory_fts MATCH ?
			ORDER BY rank
			LIMIT ?
		`, safeQuery, limit)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Entry
	now := time.Now().Unix()
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Text, &e.Tags, &e.Source, &e.CreatedAt, &e.AccessedAt, &e.AccessCount); err != nil {
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
// configured or the embedding call fails.
func (d *DB) SearchMemoryVector(ctx context.Context, query string, limit int) ([]VectorResult, error) {
	if d.embedder == nil {
		return nil, nil
	}
	qEmb, err := d.embedder.Embed(ctx, query)
	if err != nil {
		return nil, nil
	}

	rows, err := d.sql.Query(`
		SELECT id, text, tags, source, created_at, COALESCE(accessed_at, 0), access_count, COALESCE(embedding, '')
		FROM memory
		WHERE embedding IS NOT NULL
	`)
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
		if err := rows.Scan(&e.ID, &e.Text, &e.Tags, &e.Source, &e.CreatedAt, &e.AccessedAt, &e.AccessCount, &blob); err != nil {
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
// the reconciliation.
func (d *DB) ReconcileEmbeddings(ctx context.Context) (int, error) {
	if d.embedder == nil {
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
		emb, err := d.embedder.Embed(ctx, r.text)
		if err != nil {
			continue
		}
		_, err = d.sql.Exec(`UPDATE memory SET embedding = ? WHERE id = ?`, EncodeEmbedding(emb), r.id)
		if err != nil {
			continue
		}
		updated++
	}
	return updated, nil
}

// GetMemory retrieves a single memory entry by ID.
func (d *DB) GetMemory(id string) (Entry, error) {
	row := d.sql.QueryRow(`
		SELECT id, text, tags, source, created_at, COALESCE(accessed_at, 0), access_count
		FROM memory WHERE id = ?
	`, id)
	var e Entry
	err := row.Scan(&e.ID, &e.Text, &e.Tags, &e.Source, &e.CreatedAt, &e.AccessedAt, &e.AccessCount)
	return e, err
}

// DeleteMemory removes a memory entry by ID.
func (d *DB) DeleteMemory(id string) error {
	_, err := d.sql.Exec(`DELETE FROM memory WHERE id = ?`, id)
	return err
}

// UpdateMemory updates the text of an existing memory entry.
func (d *DB) UpdateMemory(id string, text string) error {
	result, err := d.sql.Exec(`UPDATE memory SET text = ?, digest = ? WHERE id = ?`, text, memoryDigest(text), id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("memory not found: %s", id)
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
	rows, err := d.sql.Query(`
		SELECT id, text, tags, source, created_at, COALESCE(accessed_at, 0), access_count
		FROM memory
		ORDER BY created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Text, &e.Tags, &e.Source, &e.CreatedAt, &e.AccessedAt, &e.AccessCount); err != nil {
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
