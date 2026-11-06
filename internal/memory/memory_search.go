package memory

import (
	"strings"
	"time"
)

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
