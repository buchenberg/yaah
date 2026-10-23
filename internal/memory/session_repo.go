package memory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrSessionNotFound is returned when a session id does not exist in the
// store (delete/resume of a stale or unknown id).
var ErrSessionNotFound = errors.New("session not found")

// ErrInvalidCursor is returned for malformed pagination cursors. Callers
// (ACP session/list) surface it as invalid params.
var ErrInvalidCursor = errors.New("invalid cursor")

// DefaultSessionPageSize caps ListSessionsPage when the caller passes no
// limit (the ACP spec: agents SHOULD enforce a sensible page size).
//
// Exported so higher layers that need to reason about a page (the session
// registry's List clamp) derive it from here instead of restating 50.
const DefaultSessionPageSize = 50

// maxSessionTitleRunes bounds the derived session title; longer first
// messages are cut with an ellipsis.
const maxSessionTitleRunes = 80

// CreateSession inserts a new session. The system_prompt column is
// capped so an unbounded prompt cannot bloat the database (review A6/S7).
// A session's activity timestamp starts at its start: pickers show
// freshly created sessions with their creation time instead of an
// epoch-zero updatedAt.
func (d *DB) CreateSession(s Session) error {
	updated := s.UpdatedAt
	if updated == 0 {
		updated = s.StartedAt
	}
	_, err := d.sql.Exec(
		`INSERT INTO sessions (id, started_at, ended_at, cwd, model, tokens_in, tokens_out, system_prompt, workspace_backend, workspace_omitted_tools, title, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.StartedAt, s.EndedAt, s.CWD, s.Model, s.TokensIn, s.TokensOut, truncateWithMarker(s.SystemPrompt, maxSessionPromptBytes),
		s.WorkspaceBackend, s.WorkspaceOmittedTools, s.Title, updated,
	)
	return err
}

// UpdateSessionWorkspace records the workspace substrate a session ran
// under and the host-only tools isolated mode omitted (decision D4 of the
// isolated-workspace plan). Called after the tool registry is final, so
// the omitted list reflects what this session actually skipped. The
// columns describe the session's most recent run; a resumed session that
// changed mode overwrites them.
func (d *DB) UpdateSessionWorkspace(id, backend, omittedTools string) error {
	_, err := d.sql.Exec(
		`UPDATE sessions SET workspace_backend = ?, workspace_omitted_tools = ? WHERE id = ?`,
		backend, omittedTools, id,
	)
	return err
}

// GetSession retrieves a single session by ID.
func (d *DB) GetSession(id string) (Session, error) {
	row := d.sql.QueryRow(`
		SELECT id, started_at, COALESCE(ended_at, 0), COALESCE(cwd, ''), COALESCE(model, ''), tokens_in, tokens_out,
			COALESCE(tokens_cached, 0), COALESCE(sub_tokens_in, 0), COALESCE(sub_tokens_out, 0),
			COALESCE(system_prompt, ''), COALESCE(compacted_summary, ''),
			COALESCE(workspace_backend, ''), COALESCE(workspace_omitted_tools, ''),
			COALESCE(title, ''), COALESCE(updated_at, 0)
		FROM sessions WHERE id = ?
	`, id)
	var s Session
	err := row.Scan(&s.ID, &s.StartedAt, &s.EndedAt, &s.CWD, &s.Model, &s.TokensIn, &s.TokensOut,
		&s.TokensCached, &s.SubTokensIn, &s.SubTokensOut,
		&s.SystemPrompt, &s.CompactedSummary, &s.WorkspaceBackend, &s.WorkspaceOmittedTools,
		&s.Title, &s.UpdatedAt)
	return s, err
}

// ListSessions returns recent sessions.
func (d *DB) ListSessions(limit int) ([]Session, error) {
	rows, err := d.sql.Query(`
		SELECT id, started_at, COALESCE(ended_at, 0), COALESCE(cwd, ''), COALESCE(model, ''), tokens_in, tokens_out,
			COALESCE(tokens_cached, 0), COALESCE(sub_tokens_in, 0), COALESCE(sub_tokens_out, 0),
			COALESCE(system_prompt, ''), COALESCE(compacted_summary, ''),
			COALESCE(workspace_backend, ''), COALESCE(workspace_omitted_tools, ''),
			COALESCE(title, ''), COALESCE(updated_at, 0)
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
		if err := rows.Scan(&s.ID, &s.StartedAt, &s.EndedAt, &s.CWD, &s.Model, &s.TokensIn, &s.TokensOut,
			&s.TokensCached, &s.SubTokensIn, &s.SubTokensOut,
			&s.SystemPrompt, &s.CompactedSummary, &s.WorkspaceBackend, &s.WorkspaceOmittedTools,
			&s.Title, &s.UpdatedAt); err != nil {
			return nil, err
		}
		results = append(results, s)
	}
	return results, rows.Err()
}

// SessionListFilter narrows a ListSessionsPage query.
type SessionListFilter struct {
	// CWD, when non-empty, matches sessions started in exactly this
	// directory (the ACP session/list cwd parameter, an absolute path).
	CWD string
	// Cursor is the opaque pagination cursor from a previous page;
	// empty for the first page.
	Cursor string
	// Limit caps the page size; <= 0 uses DefaultSessionPageSize.
	Limit int
}

// SessionSummary is the projection ListSessionsPage returns per row: the
// fields a session picker (ACP session/list, `yaah sessions`, a future
// TUI/REPL resume picker) needs without touching message bodies.
type SessionSummary struct {
	ID           string `json:"id"`
	CWD          string `json:"cwd,omitempty"`
	Title        string `json:"title,omitempty"`
	Model        string `json:"model,omitempty"`
	StartedAt    int64  `json:"started_at"`
	UpdatedAt    int64  `json:"updated_at"`
	EndedAt      int64  `json:"ended_at,omitempty"`
	MessageCount int    `json:"message_count"`
}

// cursorKey is the (base64-JSON) sort key of the last row of a returned
// page; the next page queried after it is strictly older.
type cursorKey struct {
	S int64  `json:"s"` // started_at of the last session on the page
	I string `json:"i"` // its id, breaking ties within the same second
}

// ListSessionsPage returns one page of session summaries, newest first,
// ordered by (started_at DESC, id DESC) for a stable keyset pagination.
// The second return value is the opaque cursor for the next page, empty
// on the last page. An unparsable filter.Cursor returns ErrInvalidCursor
// so callers can map it to an invalid-params error rather than something
// that looks like an internal failure.
func (d *DB) ListSessionsPage(filter SessionListFilter) ([]SessionSummary, string, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = DefaultSessionPageSize
	}

	where := `WHERE 1 = 1`
	var args []any
	if filter.CWD != "" {
		where += ` AND s.cwd = ?`
		args = append(args, filter.CWD)
	}

	var key cursorKey
	if filter.Cursor != "" {
		raw, err := base64.StdEncoding.DecodeString(filter.Cursor)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %s", ErrInvalidCursor, err)
		}
		// Only parseability counts: a zero/negative started_at stays a
		// legal key (a legacy row with started_at = 0 sorts last and
		// pages on) — rejecting it here would strand such a page with a
		// cursor the next request could never replay.
		if err := json.Unmarshal(raw, &key); err != nil || key.I == "" {
			return nil, "", ErrInvalidCursor
		}
		where += ` AND (s.started_at < ? OR (s.started_at = ? AND s.id < ?))`
		args = append(args, key.S, key.S, key.I)
	}

	// One lookahead row detects whether a next page exists.
	args = append(args, limit+1)
	rows, err := d.sql.Query(`
		SELECT s.id, COALESCE(s.cwd, ''), COALESCE(s.title, ''), COALESCE(s.model, ''),
			s.started_at,
			CASE WHEN COALESCE(s.updated_at, 0) > 0 THEN s.updated_at ELSE s.started_at END,
			COALESCE(s.ended_at, 0),
			(SELECT COUNT(*) FROM messages m WHERE m.session_id = s.id)
		FROM sessions s
		`+where+`
		ORDER BY s.started_at DESC, s.id DESC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var results []SessionSummary
	for rows.Next() {
		var sum SessionSummary
		if err := rows.Scan(&sum.ID, &sum.CWD, &sum.Title, &sum.Model,
			&sum.StartedAt, &sum.UpdatedAt, &sum.EndedAt, &sum.MessageCount); err != nil {
			return nil, "", err
		}
		results = append(results, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	next := ""
	if len(results) > limit {
		results = results[:limit]
		last := results[len(results)-1]
		cur, err := json.Marshal(cursorKey{S: last.StartedAt, I: last.ID})
		if err != nil {
			return nil, "", err
		}
		next = base64.StdEncoding.EncodeToString(cur)
	}
	return results, next, nil
}

// DeleteSession removes a session and its dependent rows atomically.
// messages has a foreign key to sessions(id) but foreign_keys
// enforcement is not guaranteed (modernc.org/sqlite defaults it off), so
// children are deleted explicitly — inside one transaction, so a mid-way
// failure never leaves an emptied session row still on the pages. todos
// is not session-keyed and memory entries are global; trace_owners rows
// pointing at the session are stale after deletion and are removed too.
func (d *DB) DeleteSession(id string) error {
	tx, err := d.sql.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin delete: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM messages WHERE session_id = ?`, id); err != nil {
		return fmt.Errorf("delete messages: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM trace_owners WHERE parent_session = ?`, id); err != nil {
		return fmt.Errorf("delete trace owners: %w", err)
	}
	res, err := tx.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrSessionNotFound
	}
	return tx.Commit()
}

// TouchSession records message activity: updated_at moves to the message
// timestamp and, while the title is still empty, the candidate becomes
// the title. Both are best-effort bookkeeping for pickers, not turn data.
func (d *DB) TouchSession(sessionID string, updatedAt int64, titleCandidate string) error {
	_, err := d.sql.Exec(`
		UPDATE sessions
		SET updated_at = ?,
			title = CASE WHEN COALESCE(title, '') = '' AND ? != '' THEN ? ELSE title END
		WHERE id = ?`,
		updatedAt, titleCandidate, titleCandidate, sessionID)
	return err
}

// titleSkipPrefixes mark synthetic messages routed through agentSession
// surfaces (steering, supervision, background dispatch); they must never
// become the session title.
var titleSkipPrefixes = []string{"[STEER]", "SUPERVISOR:", "CONFLICT:", "[BACKGROUND"}

// DeriveSessionTitle condenses a user message into a picker label:
// whitespace collapsed, synthetic traffic rejected, hard-capped at
// maxSessionTitleRunes with an ellipsis. Empty return = no title yet.
// Shared with the agent loop's persist hook, which mirrors TouchSession's
// one-shot rule to notify clients of the title transition.
func DeriveSessionTitle(content string) string {
	c := strings.Join(strings.Fields(strings.TrimSpace(content)), " ")
	if c == "" {
		return ""
	}
	for _, p := range titleSkipPrefixes {
		if strings.HasPrefix(c, p) {
			return ""
		}
	}
	if strings.HasPrefix(c, "[") {
		return ""
	}
	r := []rune(c)
	if len(r) > maxSessionTitleRunes {
		return string(r[:maxSessionTitleRunes]) + "…"
	}
	return c
}

// EndSession sets the ended_at timestamp and final token counts for a
// session, including the cache-hit and sub-agent attribution columns.
func (d *DB) EndSession(id string, endedAt int64, tokensIn int, tokensOut int, tokensCached, subTokensIn, subTokensOut int) error {
	_, err := d.sql.Exec(
		`UPDATE sessions SET ended_at = ?, tokens_in = ?, tokens_out = ?, tokens_cached = ?, sub_tokens_in = ?, sub_tokens_out = ? WHERE id = ?`,
		endedAt, tokensIn, tokensOut, tokensCached, subTokensIn, subTokensOut, id)
	return err
}

// UpdateSessionUsage flushes the running usage counters without ending
// the session. Called incrementally (per LLM call) so a session killed
// mid-run — a harness timeout, a crash — still reports what it spent.
// ended_at is untouched: the session is still open.
func (d *DB) UpdateSessionUsage(id string, tokensIn int, tokensOut int, tokensCached, subTokensIn, subTokensOut int) error {
	_, err := d.sql.Exec(
		`UPDATE sessions SET tokens_in = ?, tokens_out = ?, tokens_cached = ?, sub_tokens_in = ?, sub_tokens_out = ? WHERE id = ?`,
		tokensIn, tokensOut, tokensCached, subTokensIn, subTokensOut, id)
	return err
}

// UpdateSessionSummary persists the most recent compaction summary for
// a session, capped like system_prompt (review A6/S7).
func (d *DB) UpdateSessionSummary(id string, summary string) error {
	_, err := d.sql.Exec(`UPDATE sessions SET compacted_summary = ? WHERE id = ?`,
		truncateWithMarker(summary, maxSessionPromptBytes), id)
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
