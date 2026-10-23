package memory

import (
	"context"
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
		{"all tiers", ScopeAll, 3},
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
				if tc.scope != ScopeAll && e.Scope != tc.scope {
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

// TestScopedReadsRejectEmptyFilter pins that the storage layer no longer reads
// "" as "every tier". It used to mean "no predicate" down here while the tool
// layer read the same "" as "global only", so one input had two opposite
// meanings a hundred lines apart — the sort of ambiguity that hid this bug.
func TestScopedReadsRejectEmptyFilter(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.SearchMemoryScoped("x", 10, "", ""); err == nil {
		t.Error("SearchMemoryScoped accepted an empty scope filter")
	}
	if _, err := db.ListMemoryScoped(10, ""); err == nil {
		t.Error("ListMemoryScoped accepted an empty scope filter")
	}
	if _, err := db.SearchMemoryVectorScoped(context.Background(), "x", 10, ""); err == nil {
		t.Error("SearchMemoryVectorScoped accepted an empty scope filter")
	}
}

// TestNormalizeScopeFilter covers the read-path tier filter. It differs from
// the write-path NormalizeScope in exactly one way: it accepts ScopeAll, since
// widening a query cannot strand a row the way writing an unknown tier can.
func TestNormalizeScopeFilter(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"empty is rejected", "", "", true},
		{"global", ScopeGlobal, ScopeGlobal, false},
		{"session", ScopeSession, ScopeSession, false},
		{"all", ScopeAll, ScopeAll, false},
		{"all is case and padding insensitive", "  ALL ", ScopeAll, false},
		{"unknown tier", "project", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeScopeFilter(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizeScopeFilter(%q) = %q, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeScopeFilter(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeScopeFilter(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestScopeAllIsNotPersistable pins the one-way door that keeps ScopeAll
// honest: it is a filter, never a stored tier. A row written with scope "all"
// would be invisible to every scope = ? filter, which is the bug this whole
// bead exists to remove.
func TestScopeAllIsNotPersistable(t *testing.T) {
	if _, err := NormalizeScope(ScopeAll); err == nil {
		t.Fatal("NormalizeScope(ScopeAll) succeeded; \"all\" must stay read-path-only")
	}
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.AddMemory(Entry{ID: "a1", Text: "unfilterable", Scope: ScopeAll, CreatedAt: 1}); err == nil {
		t.Fatal("AddMemory(ScopeAll) succeeded; unfilterable rows must be impossible")
	}
}

// TestSearchMemoryScoped_AllSpansTiers verifies ScopeAll actually widens the
// query at the storage layer, which is what lets a fresh session recall atoms
// distilled by an earlier one.
func TestSearchMemoryScoped_AllSpansTiers(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	for _, e := range []Entry{
		{ID: "g1", Text: "wigwams global", Scope: ScopeGlobal, CreatedAt: 1},
		{ID: "s1", Text: "wigwams session", Scope: ScopeSession, CreatedAt: 2},
	} {
		if err := db.AddMemory(e); err != nil {
			t.Fatalf("AddMemory(%s): %v", e.ID, err)
		}
	}
	count := func(scope string) int {
		t.Helper()
		got, err := db.SearchMemoryScoped("wigwams", 10, "", scope)
		if err != nil {
			t.Fatalf("SearchMemoryScoped(%q): %v", scope, err)
		}
		return len(got)
	}
	if n := count(ScopeGlobal); n != 1 {
		t.Fatalf("global search returned %d, want 1", n)
	}
	if n := count(ScopeSession); n != 1 {
		t.Fatalf("session search returned %d, want 1", n)
	}
	if n := count(ScopeAll); n != 2 {
		t.Fatalf("all search returned %d, want 2", n)
	}
}
