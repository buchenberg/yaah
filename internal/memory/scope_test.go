package memory

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestMemoryScope_RoundTrip verifies the scope tier survives a write/read
// cycle and that an unset scope is stored as ScopeGlobal.
func TestMemoryScope_RoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	cases := []struct {
		name  string
		scope string
		want  string
	}{
		{"session", ScopeSession, ScopeSession},
		{"global", ScopeGlobal, ScopeGlobal},
		{"empty defaults to global", "", ScopeGlobal},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := Entry{
				ID:        NewEntryID(),
				Text:      "scope round-trip " + tc.name,
				Tags:      `["t"]`,
				CreatedAt: int64(i + 1),
				Scope:     tc.scope,
			}
			if err := db.AddMemory(e); err != nil {
				t.Fatalf("AddMemory: %v", err)
			}
			got, err := db.GetMemory(e.ID)
			if err != nil {
				t.Fatalf("GetMemory: %v", err)
			}
			if got.Scope != tc.want {
				t.Fatalf("GetMemory scope = %q, want %q", got.Scope, tc.want)
			}
		})
	}
}

// TestMemoryScope_AddMemoryDedupRecordsScope verifies AddMemoryDedup normalizes
// and persists the scope like AddMemory.
func TestMemoryScope_AddMemoryDedupRecordsScope(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	id := NewEntryID()
	dup, err := db.AddMemoryDedup(Entry{ID: id, Text: "dedup scoped fact", Scope: ScopeSession})
	if err != nil {
		t.Fatalf("AddMemoryDedup: %v", err)
	}
	if dup != "" {
		t.Fatalf("AddMemoryDedup reported duplicate %q for a fresh entry", dup)
	}
	got, err := db.GetMemory(id)
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if got.Scope != ScopeSession {
		t.Fatalf("scope = %q, want %q", got.Scope, ScopeSession)
	}

	defaultID := NewEntryID()
	if _, err := db.AddMemoryDedup(Entry{ID: defaultID, Text: "dedup default fact"}); err != nil {
		t.Fatalf("AddMemoryDedup: %v", err)
	}
	got, err = db.GetMemory(defaultID)
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if got.Scope != ScopeGlobal {
		t.Fatalf("default scope = %q, want %q", got.Scope, ScopeGlobal)
	}
}

// TestMemoryScope_Filters verifies that the scope filter narrows search and
// list results and that an empty filter spans every tier.
func TestMemoryScope_Filters(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	seed := []Entry{
		{ID: NewEntryID(), Text: "session fact about wigwams", CreatedAt: 1, Scope: ScopeSession},
		{ID: NewEntryID(), Text: "global fact about wigwams", CreatedAt: 2, Scope: ScopeGlobal},
		{ID: NewEntryID(), Text: "legacy fact about wigwams", CreatedAt: 3},
	}
	for _, e := range seed {
		if err := db.AddMemory(e); err != nil {
			t.Fatalf("AddMemory: %v", err)
		}
	}

	cases := []struct {
		name  string
		scope string
		want  int
	}{
		{"no filter sees every tier", "", 3},
		{"session only", ScopeSession, 1},
		{"global only", ScopeGlobal, 2},
	}
	for _, tc := range cases {
		t.Run("search "+tc.name, func(t *testing.T) {
			got, err := db.SearchMemoryScoped("wigwams", 10, "", tc.scope)
			if err != nil {
				t.Fatalf("SearchMemoryScoped: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("SearchMemoryScoped(%q) = %d results, want %d", tc.scope, len(got), tc.want)
			}
			for _, e := range got {
				if tc.scope != "" && e.Scope != tc.scope {
					t.Fatalf("result scope = %q, want %q", e.Scope, tc.scope)
				}
			}
		})
		t.Run("list "+tc.name, func(t *testing.T) {
			got, err := db.ListMemoryScoped(10, tc.scope)
			if err != nil {
				t.Fatalf("ListMemoryScoped: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("ListMemoryScoped(%q) = %d results, want %d", tc.scope, len(got), tc.want)
			}
		})
	}
}

// TestMemoryScope_MigrationFromV3 verifies the v3 → v4 forward migration: an
// existing database gains the scope column and every pre-existing row is
// backfilled to ScopeGlobal.
func TestMemoryScope_MigrationFromV3(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Build a version-3 database by hand: the pre-scope memory table, one
	// legacy row, and the version stamp the migration gate keys off.
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
			created_at   INTEGER NOT NULL,
			accessed_at  INTEGER,
			access_count INTEGER DEFAULT 0,
			digest       TEXT
		);
		INSERT INTO schema_meta (key, value) VALUES ('version', '3');
		INSERT INTO memory (id, text, tags, source, digest, created_at)
		VALUES ('mem-legacy', 'legacy fact', '[]', '', 'legacy-digest', 1);
	`); err != nil {
		t.Fatalf("seed legacy db: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close legacy: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open(migrated): %v", err)
	}
	defer db.Close()

	got, err := db.GetMemory("mem-legacy")
	if err != nil {
		t.Fatalf("GetMemory after migration: %v", err)
	}
	if got.Scope != ScopeGlobal {
		t.Fatalf("migrated scope = %q, want %q", got.Scope, ScopeGlobal)
	}

	// A database that has been migrated must accept a session-scoped write.
	fresh := Entry{ID: NewEntryID(), Text: "post-migration session atom", CreatedAt: 2, Scope: ScopeSession}
	if err := db.AddMemory(fresh); err != nil {
		t.Fatalf("AddMemory after migration: %v", err)
	}
	got, err = db.GetMemory(fresh.ID)
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if got.Scope != ScopeSession {
		t.Fatalf("scope = %q, want %q", got.Scope, ScopeSession)
	}
}
