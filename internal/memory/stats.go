package memory

import (
	"fmt"
	"sort"
	"time"
)

// stats.go is the introspection half of docs/compaction-as-sleep.md §8 P4:
// a read-only render of what the memory store has learned and when. It is
// deliberately a view over existing tables — hermes's "learning graph" is a
// render, not a store — so it never writes, and it is cheap enough to run
// from the `yaah memory stats` command.

// staleStatsAge is the floor the stats view uses to count staleness
// candidates, mirroring the hygiene sweep's 30-day floor (cmd/yaah/memory.go).
const staleStatsAge = 30 * 24 * time.Hour

// MemoryStats is the aggregate view shown by `yaah memory stats`.
type MemoryStats struct {
	Total              int   // all memory atoms
	Global             int   // scope = global
	Session            int   // scope = session
	NeverAccessed      int   // atoms no recall ever touched
	StaleCandidates    int   // compaction atoms older than the floor, never accessed
	SkillsTracked      int   // skills with a provenance row
	SkillLedgerEntries int   // append-only skill mutation rows
	OldestCreatedAt    int64 // Unix seconds; 0 when the store is empty
	NewestCreatedAt    int64 // Unix seconds; 0 when the store is empty
}

// Stats returns the aggregate memory view. Every query is a single cheap
// aggregation; nothing is written.
func (d *DB) Stats() (MemoryStats, error) {
	var s MemoryStats
	err := d.sql.QueryRow(
		`SELECT COUNT(*),
		        COALESCE(SUM(CASE WHEN scope = 'global' THEN 1 ELSE 0 END), 0),
		        COALESCE(SUM(CASE WHEN scope = 'session' THEN 1 ELSE 0 END), 0),
		        COALESCE(SUM(CASE WHEN COALESCE(access_count, 0) = 0 AND COALESCE(accessed_at, 0) = 0 THEN 1 ELSE 0 END), 0),
		        COALESCE(MIN(created_at), 0),
		        COALESCE(MAX(created_at), 0)
		   FROM memory`,
	).Scan(&s.Total, &s.Global, &s.Session, &s.NeverAccessed, &s.OldestCreatedAt, &s.NewestCreatedAt)
	if err != nil {
		return MemoryStats{}, fmt.Errorf("memory stats: %w", err)
	}

	cutoff := time.Now().Add(-staleStatsAge).Unix()
	if err := d.sql.QueryRow(
		`SELECT COUNT(*) FROM memory
		  WHERE tags LIKE ?
		    AND COALESCE(access_count, 0) = 0
		    AND COALESCE(accessed_at, 0) = 0
		    AND created_at < ?`,
		"%\""+compactionSourceTag+"\"%", cutoff,
	).Scan(&s.StaleCandidates); err != nil {
		return MemoryStats{}, fmt.Errorf("memory stats: stale candidates: %w", err)
	}

	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM skill_provenance`).Scan(&s.SkillsTracked); err != nil {
		return MemoryStats{}, fmt.Errorf("memory stats: skills: %w", err)
	}
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM skill_ledger`).Scan(&s.SkillLedgerEntries); err != nil {
		return MemoryStats{}, fmt.Errorf("memory stats: skill ledger: %w", err)
	}
	return s, nil
}

// JourneyEvent is one node of the rendered learning graph: either a memory
// atom or a skill mutation, both normalized to a common kind/timestamp so a
// viewer can draw one timeline.
type JourneyEvent struct {
	Kind      string // "memory" or "skill"
	ID        string // memory id, or skill name
	Detail    string // truncated memory text, or the skill action
	CreatedAt int64  // Unix seconds
}

// Journey returns the most recent memory and skill events merged into one
// newest-first timeline, truncated to limit (limit <= 0 means 50). It is a
// render: two bounded reads, no writes.
func (d *DB) Journey(limit int) ([]JourneyEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	var events []JourneyEvent

	rows, err := d.sql.Query(
		`SELECT id, text, created_at FROM memory ORDER BY created_at DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("journey: memories: %w", err)
	}
	for rows.Next() {
		var e JourneyEvent
		if err := rows.Scan(&e.ID, &e.Detail, &e.CreatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("journey: scan memory: %w", err)
		}
		e.Kind = "memory"
		events = append(events, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("journey: memories: %w", err)
	}

	skillRows, err := d.sql.Query(
		`SELECT name, action, created_at FROM skill_ledger ORDER BY created_at DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("journey: skills: %w", err)
	}
	for skillRows.Next() {
		var (
			name, action, createdAt string
		)
		if err := skillRows.Scan(&name, &action, &createdAt); err != nil {
			skillRows.Close()
			return nil, fmt.Errorf("journey: scan skill: %w", err)
		}
		e := JourneyEvent{Kind: "skill", ID: name, Detail: action}
		if ts, err := time.Parse(time.RFC3339, createdAt); err == nil {
			e.CreatedAt = ts.Unix()
		}
		events = append(events, e)
	}
	skillRows.Close()
	if err := skillRows.Err(); err != nil {
		return nil, fmt.Errorf("journey: skills: %w", err)
	}

	sort.Slice(events, func(i, j int) bool { return events[i].CreatedAt > events[j].CreatedAt })
	if len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}
