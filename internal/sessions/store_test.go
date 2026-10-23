package sessions

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/cwdpath"
	"github.com/buchenberg/yaah/internal/memory"
)

// cappedStore narrows the real store's page size so the registry's cursor
// chase is exercised against real keyset SQL (the store itself returns exactly
// the rows asked for, so List would otherwise reach the limit in one query).
type cappedStore struct {
	db  *memory.DB
	cap int
}

func (c cappedStore) ListSessionsPage(f memory.SessionListFilter) ([]memory.SessionSummary, string, error) {
	if f.Limit <= 0 || f.Limit > c.cap {
		f.Limit = c.cap
	}
	return c.db.ListSessionsPage(f)
}

func (c cappedStore) GetSession(id string) (memory.Session, error) {
	return dbStore{c.db}.GetSession(id)
}

func (c cappedStore) DeleteSession(id string) error { return c.db.DeleteSession(id) }

// TestRegistryIntegration is the one test that runs against a real store: the
// (started_at DESC, id DESC) keyset order survives a multi-page chase, the
// cursor round-trips into the next page, and resolve/delete map the store's
// not-found onto the registry sentinel.
func TestRegistryIntegration(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())
	db, err := memory.OpenDefault()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// A real canonicalized temp dir rather than a POSIX literal: on Windows
	// "/work" is not an absolute path, and the registry canonicalizes the
	// filter, so the stored cwd has to be its canonical form to match.
	cwd, err := cwdpath.Canonical(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize cwd: %v", err)
	}
	const n = 5
	now := time.Now().Unix()
	for i := 0; i < n; i++ {
		if err := db.CreateSession(memory.Session{
			ID:        fmt.Sprintf("sess-%d", i),
			StartedAt: now - int64(i),
			CWD:       cwd,
			Title:     fmt.Sprintf("title %d", i),
		}); err != nil {
			t.Fatalf("create session %d: %v", i, err)
		}
	}

	reg := New(cappedStore{db: db, cap: 2})

	rows, err := reg.List(Filter{CWD: cwd, Limit: n})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != n {
		t.Fatalf("rows = %d, want %d", len(rows), n)
	}
	for i, row := range rows {
		if want := fmt.Sprintf("sess-%d", i); row.ID != want {
			t.Fatalf("row %d = %s, want %s (keyset order must survive the chase)", i, row.ID, want)
		}
	}

	// The cursor round-trips: the second page continues strictly after the
	// first, with no repeats and no gaps.
	first, next, err := reg.Page(Filter{CWD: cwd, Limit: 2})
	if err != nil {
		t.Fatalf("Page 1: %v", err)
	}
	if len(first) != 2 || first[0].ID != "sess-0" {
		t.Fatalf("page 1 = %+v, want sess-0 first", first)
	}
	if next == "" {
		t.Fatal("page 1 reported no cursor although rows remain")
	}
	second, _, err := reg.Page(Filter{CWD: cwd, Cursor: next, Limit: 2})
	if err != nil {
		t.Fatalf("Page 2: %v", err)
	}
	if len(second) != 2 || second[0].ID != "sess-2" {
		t.Fatalf("page 2 = %+v, want it to continue at sess-2", second)
	}

	// A different cwd matches nothing rather than erroring.
	if other, err := reg.List(Filter{CWD: t.TempDir(), Limit: n}); err != nil || len(other) != 0 {
		t.Fatalf("other cwd = (%+v, %v), want an empty page", other, err)
	}

	// Membership and delete ride the same seam, with the store's two spellings
	// of "no such session" both mapped onto ErrNotFound.
	if _, err := reg.Resolve("sess-3"); err != nil {
		t.Fatalf("Resolve known: %v", err)
	}
	if _, err := reg.Resolve("sess-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve unknown = %v, want ErrNotFound", err)
	}
	if err := reg.Delete("sess-4"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := reg.Delete("sess-4"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete = %v, want ErrNotFound", err)
	}
}
