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
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

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
}

// DB is the yaah persistent database.
type DB struct {
	sql      *sql.DB
	embedder Embedder // optional; set via SetEmbedder
	embMu    sync.Mutex
}

// SetEmbedder configures the embedding model used for semantic search.
// When nil, vector search is disabled.
func (d *DB) SetEmbedder(e Embedder) { d.embedder = e }

// Embedder returns the configured embedding model, or nil.
func (d *DB) Embedder() Embedder { return d.embedder }

// Open opens (or creates) the yaah database at the given path.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	db, err := sql.Open("sqlite", path)
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
		PRIMARY KEY (session_id, idx)
	);

	CREATE TABLE IF NOT EXISTS memory (
		id          TEXT PRIMARY KEY,
		text        TEXT NOT NULL,
		tags        TEXT,
		source      TEXT,
		created_at  INTEGER NOT NULL,
		accessed_at INTEGER,
		access_count INTEGER DEFAULT 0
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
	CREATE TABLE IF NOT EXISTS schema_meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
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

	// Migration: add tool_call_id column for existing databases
	var hasColumn bool
	row := d.sql.QueryRow("SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'tool_call_id'")
	row.Scan(&hasColumn)
	if !hasColumn {
		d.sql.Exec("ALTER TABLE messages ADD COLUMN tool_call_id TEXT")
	}

	// Migration: add id column to messages
	row = d.sql.QueryRow("SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'id'")
	row.Scan(&hasColumn)
	if !hasColumn {
		d.sql.Exec("ALTER TABLE messages ADD COLUMN id TEXT DEFAULT ''")
	}

	// Migration: add compaction cooldown columns to sessions
	row = d.sql.QueryRow("SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'compaction_cooldown_until'")
	row.Scan(&hasColumn)
	if !hasColumn {
		d.sql.Exec("ALTER TABLE sessions ADD COLUMN compaction_cooldown_until INTEGER DEFAULT 0")
		d.sql.Exec("ALTER TABLE sessions ADD COLUMN ineffective_compactions INTEGER DEFAULT 0")
	}

	// Migration: add reasoning_content column to messages (preserves DeepSeek thinking-mode history).
	row = d.sql.QueryRow("SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'reasoning_content'")
	row.Scan(&hasColumn)
	if !hasColumn {
		d.sql.Exec("ALTER TABLE messages ADD COLUMN reasoning_content TEXT DEFAULT ''")
	}

	// Migration: add system_prompt column to sessions (session restoration).
	row = d.sql.QueryRow("SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'system_prompt'")
	row.Scan(&hasColumn)
	if !hasColumn {
		d.sql.Exec("ALTER TABLE sessions ADD COLUMN system_prompt TEXT DEFAULT ''")
	}

	// Migration: add compacted_summary column to sessions (compaction preservation).
	row = d.sql.QueryRow("SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'compacted_summary'")
	row.Scan(&hasColumn)
	if !hasColumn {
		d.sql.Exec("ALTER TABLE sessions ADD COLUMN compacted_summary TEXT DEFAULT ''")
	}

	// Migration: add embedding column to memory for vector search.
	row = d.sql.QueryRow("SELECT COUNT(*) FROM pragma_table_info('memory') WHERE name = 'embedding'")
	row.Scan(&hasColumn)
	if !hasColumn {
		d.sql.Exec("ALTER TABLE memory ADD COLUMN embedding BLOB")
	}

	// Migration: add embedding column to messages for vector search.
	row = d.sql.QueryRow("SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'embedding'")
	row.Scan(&hasColumn)
	if !hasColumn {
		d.sql.Exec("ALTER TABLE messages ADD COLUMN embedding BLOB")
	}

	return nil
}

// AddMemory inserts a new memory entry. Embedding happens in a
// background goroutine so the caller is not blocked on the model.
func (d *DB) AddMemory(e Entry) error {
	_, err := d.sql.Exec(
		`INSERT INTO memory (id, text, tags, source, created_at) VALUES (?, ?, ?, ?, ?)`,
		e.ID, e.Text, e.Tags, e.Source, e.CreatedAt,
	)
	if err == nil {
		d.embedMemoryAsync(e.ID, e.Text)
	}
	return err
}

// embedMemoryAsync embeds the text in a background goroutine and stores
// the result. Returns a channel that closes when the embed completes, or
// nil when no embedder is configured. Callers that need to wait for the
// embedding (e.g. tests) can read from the channel.
func (d *DB) embedMemoryAsync(id, text string) <-chan struct{} {
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
// if the entry was added.
func (d *DB) AddMemoryDedup(e Entry) (string, error) {
	safeQuery := sanitizeFTSQuery(e.Text)
	row := d.sql.QueryRow(`
		SELECT m.id FROM memory m
		JOIN memory_fts ON memory_fts.rowid = m.rowid
		WHERE memory_fts MATCH ? LIMIT 1
	`, safeQuery)
	var dupID string
	if err := row.Scan(&dupID); err == nil {
		return dupID, nil
	}
	return "", d.AddMemory(e)
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
		d.sql.Exec(`UPDATE memory SET access_count = access_count + 1, accessed_at = ? WHERE id = ?`, now, results[i].ID)
		results[i].AccessCount++
		results[i].AccessedAt = now
	}

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

	// Sort descending by score.
	for i := 0; i < len(results)-1; i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].Score > results[i].Score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	now := time.Now().Unix()
	for i := range results {
		d.sql.Exec(`UPDATE memory SET access_count = access_count + 1, accessed_at = ? WHERE id = ?`, now, results[i].ID)
		results[i].AccessCount++
		results[i].AccessedAt = now
	}

	return results, nil
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
	result, err := d.sql.Exec(`UPDATE memory SET text = ? WHERE id = ?`, text, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("memory not found: %s", id)
	}
	return nil
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
