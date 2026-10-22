package memory

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMigrate_rejectsNewerSchema pins the fail-fast rule: a database
// stamped by a newer yaah must be refused at open, not silently
// queried with half-supported SQL (review A6). The multi-digit case
// guards the numeric comparison — lexicographic ordering would accept
// version "10" against a binary at version "2".
func TestMigrate_rejectsNewerSchema(t *testing.T) {
	t.Run("single digit", func(t *testing.T) {
		path := t.TempDir() + "/future.db"

		db, err := Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if _, err := db.sql.Exec(`UPDATE schema_meta SET value = '999' WHERE key = 'version'`); err != nil {
			t.Fatalf("stamp version: %v", err)
		}
		db.Close()

		if _, err := Open(path); err == nil {
			t.Fatal("Open on a newer-schema database should fail")
		} else if !strings.Contains(err.Error(), "newer than this binary supports") {
			t.Fatalf("error = %v, want newer-schema message", err)
		}
	})

	t.Run("multi digit numeric compare", func(t *testing.T) {
		path := t.TempDir() + "/future10.db"

		db, err := Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if _, err := db.sql.Exec(`UPDATE schema_meta SET value = '10' WHERE key = 'version'`); err != nil {
			t.Fatalf("stamp version: %v", err)
		}
		db.Close()

		// "10" sorts before "2" lexicographically; the numeric compare
		// must still refuse it.
		if _, err := Open(path); err == nil {
			t.Fatal("Open on schema version 10 should fail against a version-1 binary")
		}
	})

	t.Run("invalid version refuses", func(t *testing.T) {
		path := t.TempDir() + "/badversion.db"

		db, err := Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if _, err := db.sql.Exec(`UPDATE schema_meta SET value = 'not-a-number' WHERE key = 'version'`); err != nil {
			t.Fatalf("stamp version: %v", err)
		}
		db.Close()

		if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "invalid memory db schema version") {
			t.Fatalf("error = %v, want invalid-version message", err)
		}
	})
}

// TestCreateSession_capsSystemPrompt pins the system_prompt cap
// (review A6/S7): unbounded prompts are truncated with a marker.
func TestCreateSession_capsSystemPrompt(t *testing.T) {
	db, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	big := strings.Repeat("x", maxSessionPromptBytes+4096)
	s := Session{ID: "sess-cap", StartedAt: 1, SystemPrompt: big}
	if err := db.CreateSession(s); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, err := db.GetSession("sess-cap")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if len(got.SystemPrompt) >= len(big) {
		t.Fatalf("system_prompt not truncated: %d bytes", len(got.SystemPrompt))
	}
	if !strings.HasSuffix(got.SystemPrompt, "[truncated]") {
		t.Errorf("truncated prompt missing marker: %q", got.SystemPrompt[len(got.SystemPrompt)-30:])
	}
}

// TestUpdateSessionSummary_capsSummary pins the compacted_summary cap.
func TestUpdateSessionSummary_capsSummary(t *testing.T) {
	db, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := db.CreateSession(Session{ID: "sess-sum", StartedAt: 1}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	big := strings.Repeat("y", maxSessionPromptBytes+4096)
	if err := db.UpdateSessionSummary("sess-sum", big); err != nil {
		t.Fatalf("UpdateSessionSummary: %v", err)
	}
	got, err := db.GetSession("sess-sum")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if len(got.CompactedSummary) >= len(big) {
		t.Fatalf("compacted_summary not truncated: %d bytes", len(got.CompactedSummary))
	}
	if !strings.HasSuffix(got.CompactedSummary, "[truncated]") {
		t.Errorf("truncated summary missing marker")
	}
}

// TestSearchMemory_batchesAccessBump verifies that searching still
// bumps access counts after the batched-update change (review A6).
func TestSearchMemory_batchesAccessBump(t *testing.T) {
	db, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	id, err := db.AddMemoryDedup(Entry{Text: "unique searchable zebra fact", Tags: `["t"]`})
	if err != nil && id == "" {
		t.Fatalf("AddMemoryDedup: %v", err)
	}
	if _, err := db.SearchMemory("zebra", 10); err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	e, err := db.GetMemory(id)
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if e.AccessCount != 1 {
		t.Errorf("access_count = %d, want 1 after one search", e.AccessCount)
	}
}

// pruneTestDB opens a throwaway database for the hygiene sweeps.
func pruneTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seedMemory writes the fixture entries, failing the test on the first error.
func seedMemory(t *testing.T, db *DB, entries []Entry) {
	t.Helper()
	for _, e := range entries {
		if err := db.AddMemory(e); err != nil {
			t.Fatalf("AddMemory(%s): %v", e.ID, err)
		}
	}
}

// pruneMemoryIDs lists every surviving atom, ordered so a test can compare
// the whole surviving set with one string equality.
func pruneMemoryIDs(t *testing.T, db *DB) []string {
	t.Helper()
	rows, err := db.sql.Query(`SELECT id FROM memory ORDER BY id`)
	if err != nil {
		t.Fatalf("list atom ids: %v", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan atom id: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list atom ids: %v", err)
	}
	return ids
}

// TestPruneSession pins the session-deletion sweep: only the target session's
// own session-scoped, never-accessed atoms go. Other sessions, global atoms
// (even ones the session wrote), unlinked atoms, and anything that was ever
// accessed all survive — access is proof the atom earned its keep, and an
// empty session id must never become a blanket delete.
func TestPruneSession(t *testing.T) {
	now := time.Now().Unix()
	seed := []Entry{
		{ID: "a-target-fresh", Text: "fresh target atom", Scope: ScopeSession, SessionID: "s1", Tags: `["source:compaction"]`, CreatedAt: now},
		{ID: "b-target-accessed-count", Text: "accessed-count target atom", Scope: ScopeSession, SessionID: "s1", CreatedAt: now},
		{ID: "c-target-accessed-at", Text: "accessed-at target atom", Scope: ScopeSession, SessionID: "s1", CreatedAt: now},
		{ID: "d-other-session", Text: "another session's atom", Scope: ScopeSession, SessionID: "s2", CreatedAt: now},
		{ID: "e-global-linked", Text: "global atom written by s1", Scope: ScopeGlobal, SessionID: "s1", Tags: `["source:compaction"]`, CreatedAt: now},
		{ID: "f-session-unlinked", Text: "session atom with no link", Scope: ScopeSession, CreatedAt: now},
	}
	all := []string{
		"a-target-fresh", "b-target-accessed-count", "c-target-accessed-at",
		"d-other-session", "e-global-linked", "f-session-unlinked",
	}
	cases := []struct {
		name        string
		sessionID   string
		wantDeleted int64
		want        []string
	}{
		{
			name:        "target session only",
			sessionID:   "s1",
			wantDeleted: 1,
			want: []string{
				"b-target-accessed-count", "c-target-accessed-at",
				"d-other-session", "e-global-linked", "f-session-unlinked",
			},
		},
		{
			name:        "empty session id is a no-op",
			sessionID:   "",
			wantDeleted: 0,
			want:        all,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := pruneTestDB(t)
			seedMemory(t, db, seed)
			// Each access signal guards on its own: a row bumped only in
			// access_count, and one marked only in accessed_at, must both
			// survive.
			if _, err := db.sql.Exec(`UPDATE memory SET access_count = 1 WHERE id = 'b-target-accessed-count'`); err != nil {
				t.Fatalf("mark access_count: %v", err)
			}
			if _, err := db.sql.Exec(`UPDATE memory SET accessed_at = ? WHERE id = 'c-target-accessed-at'`, now); err != nil {
				t.Fatalf("mark accessed_at: %v", err)
			}

			n, err := db.PruneSession(tc.sessionID)
			if err != nil {
				t.Fatalf("PruneSession(%q): %v", tc.sessionID, err)
			}
			if n != tc.wantDeleted {
				t.Errorf("PruneSession(%q) deleted %d rows, want %d", tc.sessionID, n, tc.wantDeleted)
			}
			if got := strings.Join(pruneMemoryIDs(t, db), ","); got != strings.Join(tc.want, ",") {
				t.Errorf("surviving atoms = %s, want %s", got, strings.Join(tc.want, ","))
			}
		})
	}
}

// TestPruneStaleCompaction pins the slow sweep: only old, never-accessed,
// compaction-tagged atoms go, whatever their scope. Each guard rules out one
// mistake — recent atoms may still be recalled, an accessed atom earned its
// keep, a non-compaction tag marks a deliberate write, and a non-compaction
// tag on a session atom is still never swept.
func TestPruneStaleCompaction(t *testing.T) {
	now := time.Now()
	old := now.Add(-60 * 24 * time.Hour).Unix()
	recent := now.Add(-24 * time.Hour).Unix()
	cutoff := now.Add(-30 * 24 * time.Hour)

	seed := []Entry{
		{ID: "a-old-compaction", Text: "dead deep-sleep atom", Scope: ScopeGlobal, Tags: `["preference","source:compaction"]`, CreatedAt: old},
		{ID: "b-old-compaction-accessed-count", Text: "recalled deep-sleep atom", Scope: ScopeGlobal, Tags: `["source:compaction"]`, CreatedAt: old},
		{ID: "c-old-compaction-accessed-at", Text: "recalled deep-sleep atom 2", Scope: ScopeGlobal, Tags: `["source:compaction"]`, CreatedAt: old},
		{ID: "d-old-plain", Text: "deliberate global atom", Scope: ScopeGlobal, Tags: `["preference"]`, CreatedAt: old},
		{ID: "e-old-session-compaction", Text: "session residue", Scope: ScopeSession, SessionID: "s1", Tags: `["source:compaction"]`, CreatedAt: old},
		{ID: "f-recent-compaction", Text: "fresh deep-sleep atom", Scope: ScopeGlobal, Tags: `["source:compaction"]`, CreatedAt: recent},
	}
	all := []string{
		"a-old-compaction", "b-old-compaction-accessed-count", "c-old-compaction-accessed-at",
		"d-old-plain", "e-old-session-compaction", "f-recent-compaction",
	}
	cases := []struct {
		name        string
		olderThan   time.Time
		wantDeleted int64
		want        []string
	}{
		{
			name:        "old never-accessed compaction atoms of any scope",
			olderThan:   cutoff,
			wantDeleted: 2,
			want: []string{
				"b-old-compaction-accessed-count", "c-old-compaction-accessed-at",
				"d-old-plain", "f-recent-compaction",
			},
		},
		{
			name:        "zero cutoff deletes nothing",
			olderThan:   time.Time{},
			wantDeleted: 0,
			want:        all,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := pruneTestDB(t)
			seedMemory(t, db, seed)
			if _, err := db.sql.Exec(`UPDATE memory SET access_count = 1 WHERE id = 'b-old-compaction-accessed-count'`); err != nil {
				t.Fatalf("mark access_count: %v", err)
			}
			if _, err := db.sql.Exec(`UPDATE memory SET accessed_at = ? WHERE id = 'c-old-compaction-accessed-at'`, now.Unix()); err != nil {
				t.Fatalf("mark accessed_at: %v", err)
			}

			n, err := db.PruneStaleCompaction(tc.olderThan)
			if err != nil {
				t.Fatalf("PruneStaleCompaction: %v", err)
			}
			if n != tc.wantDeleted {
				t.Errorf("PruneStaleCompaction deleted %d rows, want %d", n, tc.wantDeleted)
			}
			if got := strings.Join(pruneMemoryIDs(t, db), ","); got != strings.Join(tc.want, ",") {
				t.Errorf("surviving atoms = %s, want %s", got, strings.Join(tc.want, ","))
			}
		})
	}
}

// TestMigrate_v4ToV5_addsSessionID verifies the v4 → v5 forward migration: an
// existing database gains the nullable session_id column, its rows keep a
// NULL link (so PruneSession can never attribute them), and new writes can
// record one.
func TestMigrate_v4ToV5_addsSessionID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v4.db")

	// Build a version-4 database by hand: the pre-session-link memory table,
	// one legacy row, and the version stamp the migration gate keys off.
	raw, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		CREATE TABLE memory (
			id           TEXT PRIMARY KEY,
			text         TEXT NOT NULL,
			tags         TEXT,
			source       TEXT,
			scope        TEXT NOT NULL DEFAULT 'global',
			created_at   INTEGER NOT NULL,
			accessed_at  INTEGER,
			access_count INTEGER DEFAULT 0,
			digest       TEXT
		);
		INSERT INTO schema_meta (key, value) VALUES ('version', '4');
		INSERT INTO memory (id, text, tags, source, scope, digest, created_at)
		VALUES ('mem-v4', 'pre-session-link fact', '["source:compaction"]', 'agent', 'global', 'v4-digest', 1);
	`); err != nil {
		t.Fatalf("seed v4 db: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close legacy: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open(migrated): %v", err)
	}
	defer db.Close()

	var has int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('memory') WHERE name = 'session_id'`).Scan(&has); err != nil {
		t.Fatalf("check session_id column: %v", err)
	}
	if has != 1 {
		t.Fatalf("memory.session_id missing after migration (count = %d)", has)
	}

	var version string
	if err := db.sql.QueryRow(`SELECT value FROM schema_meta WHERE key = 'version'`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if want := strconv.Itoa(currentSchemaVersion); version != want {
		t.Errorf("schema version = %q, want %q", version, want)
	}

	legacy, err := db.GetMemory("mem-v4")
	if err != nil {
		t.Fatalf("GetMemory(legacy): %v", err)
	}
	if legacy.SessionID != "" {
		t.Errorf("legacy SessionID = %q, want empty (a pre-v5 row carries no link)", legacy.SessionID)
	}

	fresh := Entry{ID: "mem-v5", Text: "post-migration session atom", Scope: ScopeSession, SessionID: "sess-v5", CreatedAt: 2}
	if err := db.AddMemory(fresh); err != nil {
		t.Fatalf("AddMemory after migration: %v", err)
	}
	got, err := db.GetMemory(fresh.ID)
	if err != nil {
		t.Fatalf("GetMemory(new): %v", err)
	}
	if got.SessionID != "sess-v5" {
		t.Errorf("SessionID = %q, want %q", got.SessionID, "sess-v5")
	}
}
