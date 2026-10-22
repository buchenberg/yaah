package memory

import (
	"strings"
	"testing"
	"time"
)

func TestStats(t *testing.T) {
	db := pruneTestDB(t)
	now := time.Now().Unix()

	seedMemory(t, db, []Entry{
		{ID: "g1", Text: "global never accessed", Scope: ScopeGlobal, CreatedAt: now - 1000},
		{ID: "s1", Text: "session never accessed", Scope: ScopeSession, SessionID: "sess", CreatedAt: now - 500},
		{ID: "g2", Text: "global accessed", Scope: ScopeGlobal, CreatedAt: now},
		{
			ID:        "stale",
			Text:      "dead deep-sleep atom",
			Scope:     ScopeGlobal,
			Tags:      `["source:compaction"]`,
			CreatedAt: time.Now().Add(-31 * 24 * time.Hour).Unix(),
		},
	})
	// g2 earned its keep.
	if _, err := db.sql.Exec(`UPDATE memory SET access_count = 1 WHERE id = 'g2'`); err != nil {
		t.Fatalf("mark g2 accessed: %v", err)
	}
	if err := db.RecordSkillProvenance("tdd", "agent", "skill:create"); err != nil {
		t.Fatalf("RecordSkillProvenance: %v", err)
	}
	if err := db.RecordSkillProvenance("review", "agent", "skill:create"); err != nil {
		t.Fatalf("RecordSkillProvenance: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := db.RecordSkillLedger("tdd", "edit", "a", "b", ""); err != nil {
			t.Fatalf("RecordSkillLedger: %v", err)
		}
	}

	stats, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Total != 4 || stats.Global != 3 || stats.Session != 1 {
		t.Errorf("totals = %+v, want total=4 global=3 session=1", stats)
	}
	if stats.NeverAccessed != 3 {
		t.Errorf("NeverAccessed = %d, want 3", stats.NeverAccessed)
	}
	if stats.StaleCandidates != 1 {
		t.Errorf("StaleCandidates = %d, want 1", stats.StaleCandidates)
	}
	if stats.SkillsTracked != 2 || stats.SkillLedgerEntries != 3 {
		t.Errorf("skills = tracked:%d ledger:%d, want 2/3", stats.SkillsTracked, stats.SkillLedgerEntries)
	}
	if stats.OldestCreatedAt == 0 || stats.NewestCreatedAt == 0 {
		t.Errorf("oldest/newest not populated: %+v", stats)
	}
}

func TestStatsEmpty(t *testing.T) {
	db := pruneTestDB(t)
	stats, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats on empty store: %v", err)
	}
	if stats.Total != 0 || stats.OldestCreatedAt != 0 || stats.NewestCreatedAt != 0 {
		t.Errorf("empty stats = %+v, want zero view", stats)
	}
}

func TestJourney(t *testing.T) {
	db := pruneTestDB(t)
	seedMemory(t, db, []Entry{
		{ID: "old", Text: "oldest memory", Scope: ScopeGlobal, CreatedAt: 100},
		{ID: "new", Text: "newer memory", Scope: ScopeGlobal, CreatedAt: 300},
	})
	if err := db.RecordSkillProvenance("tdd", "agent", "skill:create"); err != nil {
		t.Fatalf("RecordSkillProvenance: %v", err)
	}
	// Skill ledger entries carry RFC 3339 "now" timestamps, so they are always
	// the newest events in a seeded journey.
	if err := db.RecordSkillLedger("tdd", "create", "", "h", ""); err != nil {
		t.Fatalf("RecordSkillLedger: %v", err)
	}

	events, err := db.Journey(10)
	if err != nil {
		t.Fatalf("Journey: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("Journey len = %d, want 3", len(events))
	}
	if events[0].Kind != "skill" || events[0].ID != "tdd" {
		t.Errorf("newest event = %+v, want the skill ledger entry", events[0])
	}
	if events[1].Kind != "memory" || events[1].ID != "new" || events[2].Kind != "memory" || events[2].ID != "old" {
		t.Errorf("memory order = %s then %s, want new then old", events[1].ID, events[2].ID)
	}
	for _, e := range events {
		if e.CreatedAt == 0 && e.Kind == "skill" {
			t.Errorf("skill event has no parsed timestamp: %+v", e)
		}
	}

	limited, err := db.Journey(2)
	if err != nil {
		t.Fatalf("Journey(2): %v", err)
	}
	if len(limited) != 2 || limited[0].Kind != "skill" || limited[1].ID != "new" {
		t.Errorf("Journey(2) = %+v, want skill + newest memory", limited)
	}
	if strings.TrimSpace(limited[1].Detail) == "" {
		t.Errorf("memory event missing detail text")
	}
}
