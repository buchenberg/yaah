package memory

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	ID        string `json:"id"`
	StartedAt int64  `json:"started_at"`
	EndedAt   int64  `json:"ended_at,omitempty"`
	CWD       string `json:"cwd"`
	Model     string `json:"model"`
	TokensIn  int    `json:"tokens_in"`
	TokensOut int    `json:"tokens_out"`
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
	sql *sql.DB
}

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
	// Split on whitespace, quote each term
	words := strings.Fields(query)
	var quoted []string
	for _, w := range words {
		// Escape double quotes inside the term
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

	return nil
}

// AddMemory inserts a new memory entry.
func (d *DB) AddMemory(e Entry) error {
	_, err := d.sql.Exec(
		`INSERT INTO memory (id, text, tags, source, created_at) VALUES (?, ?, ?, ?, ?)`,
		e.ID, e.Text, e.Tags, e.Source, e.CreatedAt,
	)
	return err
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

// CreateSession inserts a new session.
func (d *DB) CreateSession(s Session) error {
	_, err := d.sql.Exec(
		`INSERT INTO sessions (id, started_at, ended_at, cwd, model, tokens_in, tokens_out) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.StartedAt, s.EndedAt, s.CWD, s.Model, s.TokensIn, s.TokensOut,
	)
	return err
}

// ListSessions returns recent sessions.
func (d *DB) ListSessions(limit int) ([]Session, error) {
	rows, err := d.sql.Query(`
		SELECT id, started_at, COALESCE(ended_at, 0), cwd, model, tokens_in, tokens_out
		FROM sessions
		ORDER BY started_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.StartedAt, &s.EndedAt, &s.CWD, &s.Model, &s.TokensIn, &s.TokensOut); err != nil {
			return nil, err
		}
		results = append(results, s)
	}
	return results, rows.Err()
}

// EndSession sets the ended_at timestamp and final token counts for a session.
func (d *DB) EndSession(id string, endedAt int64, tokensIn int, tokensOut int) error {
	_, err := d.sql.Exec(`UPDATE sessions SET ended_at = ?, tokens_in = ?, tokens_out = ? WHERE id = ?`, endedAt, tokensIn, tokensOut, id)
	return err
}

// GetCompactionCooldown returns the compaction cooldown state for a session.
func (d *DB) GetCompactionCooldown(sessionID string) (cooldownUntil int64, ineffective int, err error) {
	row := d.sql.QueryRow(
		`SELECT COALESCE(compaction_cooldown_until, 0), COALESCE(ineffective_compactions, 0) FROM sessions WHERE id = ?`,
		sessionID,
	)
	err = row.Scan(&cooldownUntil, &ineffective)
	if err != nil {
		return 0, 0, err
	}
	return
}

// SetCompactionCooldown persists the compaction cooldown state for a session.
func (d *DB) SetCompactionCooldown(sessionID string, cooldownUntil int64, ineffective int) error {
	_, err := d.sql.Exec(
		`UPDATE sessions SET compaction_cooldown_until = ?, ineffective_compactions = ? WHERE id = ?`,
		cooldownUntil, ineffective, sessionID,
	)
	return err
}

// AddMessage inserts a message into a session.
func (d *DB) AddMessage(m Message) error {
	_, err := d.sql.Exec(
		`INSERT INTO messages (session_id, idx, role, content, reasoning_content, tool_name, tool_call_id, tool_calls, ts, id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.SessionID, m.Idx, m.Role, m.Content, m.ReasoningContent, m.ToolName, m.ToolCallID, m.ToolCalls, m.Timestamp, m.ID,
	)
	return err
}

// GetMessages returns all messages for a session.
func (d *DB) GetMessages(sessionID string) ([]Message, error) {
	rows, err := d.sql.Query(`
		SELECT session_id, idx, role, content, COALESCE(reasoning_content, ''), tool_name, COALESCE(tool_call_id, ''), tool_calls, ts, COALESCE(id, '')
		FROM messages
		WHERE session_id = ?
		ORDER BY idx
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.SessionID, &m.Idx, &m.Role, &m.Content, &m.ReasoningContent, &m.ToolName, &m.ToolCallID, &m.ToolCalls, &m.Timestamp, &m.ID); err != nil {
			return nil, err
		}
		results = append(results, m)
	}
	return results, rows.Err()
}

// SearchMessages searches past session messages using FTS5.
func (d *DB) SearchMessages(query string, limit int) ([]Message, error) {
	safeQuery := sanitizeFTSQuery(query)
	rows, err := d.sql.Query(`
		SELECT m.session_id, m.idx, m.role, m.content, COALESCE(m.reasoning_content, ''), m.tool_name, COALESCE(m.tool_call_id, ''), m.tool_calls, m.ts, COALESCE(m.id, '')
		FROM messages m
		JOIN messages_fts ON messages_fts.rowid = m.rowid
		WHERE messages_fts MATCH ?
		ORDER BY rank
		LIMIT ?
	`, safeQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.SessionID, &m.Idx, &m.Role, &m.Content, &m.ReasoningContent, &m.ToolName, &m.ToolCallID, &m.ToolCalls, &m.Timestamp, &m.ID); err != nil {
			return nil, err
		}
		results = append(results, m)
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
