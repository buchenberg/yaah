package sessions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/buchenberg/yaah/internal/cwdpath"
	"github.com/buchenberg/yaah/internal/memory"
)

// fakeStore is an in-memory Store: no SQLite, no cmd wiring. It records every
// query so tests can pin how many store calls a registry method makes (the
// difference between Page and List is exactly that).
type fakeStore struct {
	calls []memory.SessionListFilter

	// pageFn answers a list query; nil yields an empty last page.
	pageFn func(filter memory.SessionListFilter) ([]memory.SessionSummary, string, error)

	sessions  map[string]memory.Session
	deleteErr error
}

func (f *fakeStore) ListSessionsPage(filter memory.SessionListFilter) ([]memory.SessionSummary, string, error) {
	f.calls = append(f.calls, filter)
	if f.pageFn == nil {
		return nil, "", nil
	}
	return f.pageFn(filter)
}

func (f *fakeStore) GetSession(id string) (memory.Session, error) {
	s, ok := f.sessions[id]
	if !ok {
		return memory.Session{}, fmt.Errorf("%w: %s", memory.ErrSessionNotFound, id)
	}
	return s, nil
}

func (f *fakeStore) DeleteSession(id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.sessions[id]; !ok {
		return memory.ErrSessionNotFound
	}
	delete(f.sessions, id)
	return nil
}

// chunkStore serves at most chunk rows per query — a store is free to return
// fewer rows than asked, and that is what forces the registry to chase. The
// cursor is the index of the next row, so the chain is observable.
type chunkStore struct {
	rows  []memory.SessionSummary
	chunk int
	calls []memory.SessionListFilter
}

func (c *chunkStore) ListSessionsPage(filter memory.SessionListFilter) ([]memory.SessionSummary, string, error) {
	c.calls = append(c.calls, filter)
	start := 0
	if filter.Cursor != "" {
		n, err := strconv.Atoi(filter.Cursor)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %s", memory.ErrInvalidCursor, err)
		}
		start = n
	}
	limit := filter.Limit
	if limit <= 0 || limit > c.chunk {
		limit = c.chunk
	}
	end := start + limit
	if end > len(c.rows) {
		end = len(c.rows)
	}
	next := ""
	if end < len(c.rows) {
		next = strconv.Itoa(end)
	}
	return c.rows[start:end], next, nil
}

func (c *chunkStore) GetSession(id string) (memory.Session, error) {
	return memory.Session{ID: id}, nil
}

func (c *chunkStore) DeleteSession(string) error { return nil }

// summaryRows builds n summary rows with descending StartedAt.
func summaryRows(n int) []memory.SessionSummary {
	rows := make([]memory.SessionSummary, n)
	for i := range rows {
		rows[i] = memory.SessionSummary{ID: fmt.Sprintf("sess-%02d", i), StartedAt: int64(1000 - i)}
	}
	return rows
}

// TestRegistryPageIsOneStorePage pins the primitive: fields pass through
// untouched, the store is called exactly once (no chasing), and its cursor is
// returned verbatim.
func TestRegistryPageIsOneStorePage(t *testing.T) {
	rows := summaryRows(2)
	store := &fakeStore{pageFn: func(filter memory.SessionListFilter) ([]memory.SessionSummary, string, error) {
		if filter.Cursor != "in-cursor" || filter.Limit != 2 {
			t.Fatalf("filter = %+v, want the caller's cursor/limit", filter)
		}
		return rows, "out-cursor", nil
	}}
	reg := New(store)

	got, next, err := reg.Page(Filter{Cursor: "in-cursor", Limit: 2})
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(got) != 2 || got[0].ID != "sess-00" || got[1].ID != "sess-01" {
		t.Fatalf("rows = %+v, want the store's page verbatim", got)
	}
	if next != "out-cursor" {
		t.Fatalf("next = %q, want out-cursor", next)
	}
	if len(store.calls) != 1 {
		t.Fatalf("store calls = %d, want 1 (Page never chases)", len(store.calls))
	}
}

// TestRegistryPageCanonicalizesFilter pins that the registry hands the store a
// canonical cwd, not the caller's spelling.
func TestRegistryPageCanonicalizesFilter(t *testing.T) {
	dir := t.TempDir()
	store := &fakeStore{}
	reg := New(store)

	if _, _, err := reg.Page(Filter{CWD: filepath.Join(dir, "..", filepath.Base(dir))}); err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(store.calls) != 1 {
		t.Fatalf("store calls = %d, want 1", len(store.calls))
	}
	want, err := cwdpath.Canonical(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.calls[0].CWD; got != want {
		t.Fatalf("store cwd = %q, want canonical %q", got, want)
	}
}

// TestRegistryListChasesToLimit pins the CLI/picker path: three short pages
// are chained through their cursors until the requested limit is reached.
func TestRegistryListChasesToLimit(t *testing.T) {
	store := &chunkStore{rows: summaryRows(10), chunk: 1}
	reg := New(store)

	rows, err := reg.List(Filter{Limit: 3})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	for i, want := range []string{"sess-00", "sess-01", "sess-02"} {
		if rows[i].ID != want {
			t.Fatalf("row %d = %s, want %s (store order preserved)", i, rows[i].ID, want)
		}
	}
	if len(store.calls) != 3 {
		t.Fatalf("store calls = %d, want 3", len(store.calls))
	}
	for i, want := range []string{"", "1", "2"} {
		if got := store.calls[i].Cursor; got != want {
			t.Fatalf("call %d cursor = %q, want %q", i, got, want)
		}
	}
}

// TestRegistryListStopsOnEmptyPageWithCursor is the regression pin: a store
// page with no rows ends the list even when it reports a cursor. Trusting the
// cursor there would chase forever.
func TestRegistryListStopsOnEmptyPageWithCursor(t *testing.T) {
	store := &fakeStore{pageFn: func(memory.SessionListFilter) ([]memory.SessionSummary, string, error) {
		return nil, "a-cursor-that-never-ends", nil
	}}
	reg := New(store)

	rows, err := reg.List(Filter{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
	if len(store.calls) != 1 {
		t.Fatalf("store calls = %d, want 1 (empty page ends the chase)", len(store.calls))
	}
}

// TestRegistryListStopsOnLastPage pins the normal termination: a page whose
// cursor is empty ends the list without another query.
func TestRegistryListStopsOnLastPage(t *testing.T) {
	store := &chunkStore{rows: summaryRows(4), chunk: 2}
	reg := New(store)

	rows, err := reg.List(Filter{Limit: 40})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4", len(rows))
	}
	if len(store.calls) != 2 {
		t.Fatalf("store calls = %d, want 2", len(store.calls))
	}
}

// TestRegistryListRespectsMaxChasePages pins the loop bound: a store that never
// runs dry is abandoned after maxChasePages queries.
func TestRegistryListRespectsMaxChasePages(t *testing.T) {
	store := &chunkStore{rows: summaryRows(maxChasePages * 4), chunk: 1}
	reg := New(store)

	rows, err := reg.List(Filter{Limit: maxChasePages * 4})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(store.calls) != maxChasePages {
		t.Fatalf("store calls = %d, want maxChasePages (%d)", len(store.calls), maxChasePages)
	}
	if len(rows) != maxChasePages {
		t.Fatalf("rows = %d, want %d", len(rows), maxChasePages)
	}
}

// TestRegistryListCwdRawFallback pins the picker fallback both ways: legacy
// rows stored under the raw (un-canonicalized) spelling are found only when
// the fallback is requested, and only after the canonical pass came back
// empty.
func TestRegistryListCwdRawFallback(t *testing.T) {
	dir := t.TempDir()
	canonical, err := cwdpath.Canonical(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A symlink is the realistic non-canonical spelling: the process sees the
	// link, the store holds the resolved target. Joining ".."/base does not
	// work here — filepath.Join cleans it straight back to the canonical form,
	// which made this test skip every run and left the fallback unexercised.
	raw := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(canonical, raw); err != nil {
		t.Skipf("cannot create symlink on this platform: %v", err)
	}
	legacy := memory.SessionSummary{ID: "sess-legacy", CWD: raw}
	store := &fakeStore{pageFn: func(filter memory.SessionListFilter) ([]memory.SessionSummary, string, error) {
		if filter.CWD == raw {
			return []memory.SessionSummary{legacy}, "", nil
		}
		return nil, "", nil
	}}

	t.Run("enabled", func(t *testing.T) {
		store.calls = nil
		rows, err := New(store).List(Filter{CWD: raw, CwdRawFallback: true, Limit: 5})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(rows) != 1 || rows[0].ID != "sess-legacy" {
			t.Fatalf("rows = %+v, want the legacy row via the raw fallback", rows)
		}
		if len(store.calls) != 2 {
			t.Fatalf("store calls = %d, want 2 (canonical then raw)", len(store.calls))
		}
		if store.calls[0].CWD != canonical {
			t.Fatalf("first query cwd = %q, want canonical %q", store.calls[0].CWD, canonical)
		}
		if store.calls[1].CWD != raw {
			t.Fatalf("fallback query cwd = %q, want the raw spelling %q", store.calls[1].CWD, raw)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		store.calls = nil
		rows, err := New(store).List(Filter{CWD: raw, Limit: 5})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(rows) != 0 {
			t.Fatalf("rows = %+v, want none without the fallback", rows)
		}
		if len(store.calls) != 1 {
			t.Fatalf("store calls = %d, want 1", len(store.calls))
		}
	})

	t.Run("canonical pass wins", func(t *testing.T) {
		hitStore := &fakeStore{pageFn: func(memory.SessionListFilter) ([]memory.SessionSummary, string, error) {
			return []memory.SessionSummary{{ID: "sess-canonical"}}, "", nil
		}}
		rows, err := New(hitStore).List(Filter{CWD: raw, CwdRawFallback: true, Limit: 5})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(rows) != 1 || rows[0].ID != "sess-canonical" {
			t.Fatalf("rows = %+v, want the canonical hit", rows)
		}
		if len(hitStore.calls) != 1 {
			t.Fatalf("store calls = %d, want 1 (no fallback after a hit)", len(hitStore.calls))
		}
	})
}

// TestRegistryCWDValidation is the filter table: empty means "any cwd", a
// relative path is rejected, absolute paths are canonicalized (symlinks
// resolved, redundant segments collapsed), and a directory that does not exist
// is still a legal filter.
func TestRegistryCWDValidation(t *testing.T) {
	dir := t.TempDir()
	symlinks := runtime.GOOS != "windows" // creating symlinks needs elevation there
	link := filepath.Join(dir, "link")
	if symlinks {
		if err := os.Symlink(dir, link); err != nil {
			symlinks = false
		}
	}

	tests := []struct {
		name    string
		cwd     string
		want    string // expected store filter; empty means "unchanged input"
		wantErr bool
	}{
		{name: "empty matches any", cwd: ""},
		{name: "relative rejected", cwd: "relative", wantErr: true},
		{name: "redundant segments collapsed", cwd: filepath.Join(dir, ".", "sub", "..")},
		{name: "missing directory allowed", cwd: filepath.Join(dir, "does-not-exist")},
	}
	if symlinks {
		tests = append(tests, struct {
			name    string
			cwd     string
			want    string
			wantErr bool
		}{name: "symlink resolved", cwd: link})
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			_, _, err := New(store).Page(Filter{CWD: tc.cwd})
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidCWD) {
					t.Fatalf("err = %v, want ErrInvalidCWD", err)
				}
				if len(store.calls) != 0 {
					t.Fatalf("store calls = %d, want 0 on a rejected filter", len(store.calls))
				}
				return
			}
			if err != nil {
				t.Fatalf("Page: %v", err)
			}
			if len(store.calls) != 1 {
				t.Fatalf("store calls = %d, want 1", len(store.calls))
			}
			got := store.calls[0].CWD
			switch {
			case tc.cwd == "":
				if got != "" {
					t.Fatalf("store cwd = %q, want the empty (match-any) filter", got)
				}
			case tc.cwd == link:
				want, err := cwdpath.Canonical(dir)
				if err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Fatalf("store cwd = %q, want the resolved target %q", got, want)
				}
			default:
				want, err := cwdpath.Canonical(tc.cwd)
				if err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Fatalf("store cwd = %q, want %q", got, want)
				}
			}
		})
	}
}

// TestRegistryPageIdempotentOnCanonicalInput pins that canonicalizing an
// already-canonical filter changes nothing and never errors — the ACP server
// canonicalizes a filter on intake and the registry does it again, so the
// second pass must be a no-op.
func TestRegistryPageIdempotentOnCanonicalInput(t *testing.T) {
	dir := t.TempDir()
	once, err := cwdpath.Canonical(dir)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := cwdpath.Canonical(once)
	if err != nil {
		t.Fatalf("second pass on canonical input: %v", err)
	}
	if once != twice {
		t.Fatalf("canonical(%q) = %q, want idempotent", once, twice)
	}

	store := &fakeStore{}
	reg := New(store)
	for i := 0; i < 2; i++ {
		if _, _, err := reg.Page(Filter{CWD: once}); err != nil {
			t.Fatalf("Page pass %d: %v", i, err)
		}
	}
	if len(store.calls) != 2 {
		t.Fatalf("store calls = %d, want 2", len(store.calls))
	}
	if store.calls[0].CWD != store.calls[1].CWD || store.calls[0].CWD != once {
		t.Fatalf("store cwds = %q/%q, want both %q", store.calls[0].CWD, store.calls[1].CWD, once)
	}
}

// TestRegistryInvalidCursorMatchesBothSentinels pins the double-wrap: a view
// matches sessions.ErrInvalidCursor while the store-level sentinel stays in the
// chain for callers that already checked it.
func TestRegistryInvalidCursorMatchesBothSentinels(t *testing.T) {
	store := &fakeStore{pageFn: func(memory.SessionListFilter) ([]memory.SessionSummary, string, error) {
		return nil, "", fmt.Errorf("%w: bad base64", memory.ErrInvalidCursor)
	}}
	reg := New(store)

	_, _, err := reg.Page(Filter{Cursor: "@@@@@"})
	if err == nil {
		t.Fatal("invalid cursor must error")
	}
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("err = %v, want it to match sessions.ErrInvalidCursor", err)
	}
	if !errors.Is(err, memory.ErrInvalidCursor) {
		t.Fatalf("err = %v, want it to match memory.ErrInvalidCursor", err)
	}

	// List surfaces the same mapping.
	if _, err := reg.List(Filter{Cursor: "@@@@@"}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("List err = %v, want ErrInvalidCursor", err)
	}
}

// TestRegistryInvalidCWDKeepsCanonicalizerMessage pins that the rejection text
// survives the sentinel wrap: surfaces format their own prefix around it.
func TestRegistryInvalidCWDKeepsCanonicalizerMessage(t *testing.T) {
	_, _, err := New(&fakeStore{}).Page(Filter{CWD: "relative"})
	if !errors.Is(err, ErrInvalidCWD) {
		t.Fatalf("err = %v, want ErrInvalidCWD", err)
	}
	if want := `invalid session cwd: cwd "relative" must be an absolute path`; err.Error() != want {
		t.Fatalf("err = %q, want %q", err.Error(), want)
	}
}

// TestRegistryDelete pins the delete contract: an unknown id wraps both
// sentinels, a known id removes the row.
func TestRegistryDelete(t *testing.T) {
	store := &fakeStore{sessions: map[string]memory.Session{"sess-1": {ID: "sess-1"}}}
	reg := New(store)

	if err := reg.Delete("sess-1"); err != nil {
		t.Fatalf("Delete known: %v", err)
	}
	if _, ok := store.sessions["sess-1"]; ok {
		t.Fatal("Delete left the row in place")
	}

	err := reg.Delete("sess-missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete unknown = %v, want ErrNotFound", err)
	}
	if !errors.Is(err, memory.ErrSessionNotFound) {
		t.Fatalf("Delete unknown = %v, want it to match memory.ErrSessionNotFound", err)
	}

	store.deleteErr = errors.New("disk on fire")
	if err := reg.Delete("sess-1"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("infra error = %v, want it passed through", err)
	}
}

// TestRegistryResolve pins membership: a known id returns its row, an unknown
// id wraps ErrNotFound, and no store error type leaks to the caller.
func TestRegistryResolve(t *testing.T) {
	store := &fakeStore{sessions: map[string]memory.Session{
		"sess-1": {ID: "sess-1", CWD: "/tmp/x", Title: "hello"},
	}}
	reg := New(store)

	sess, err := reg.Resolve("sess-1")
	if err != nil {
		t.Fatalf("Resolve known: %v", err)
	}
	if sess.ID != "sess-1" || sess.Title != "hello" {
		t.Fatalf("session = %+v, want the stored row", sess)
	}
	if _, err := reg.Resolve("sess-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve unknown = %v, want ErrNotFound", err)
	}
}

// TestRegistryClose pins the ownership rule: a borrowed store is never closed,
// an owned handle is closed, and either way Close is idempotent.
func TestRegistryClose(t *testing.T) {
	store := &fakeStore{}
	reg := New(store)
	if err := reg.Close(); err != nil {
		t.Fatalf("borrowed Close: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("borrowed Close (second): %v", err)
	}

	closes := 0
	owned := &Registry{store: store, closer: func() error {
		closes++
		return nil
	}}
	if err := owned.Close(); err != nil {
		t.Fatalf("owned Close: %v", err)
	}
	if err := owned.Close(); err != nil {
		t.Fatalf("owned Close (second): %v", err)
	}
	if closes != 1 {
		t.Fatalf("closer ran %d time(s), want exactly 1", closes)
	}

	var nilReg *Registry
	if err := nilReg.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
}

// TestRegistryOpenDefault pins that OpenDefault really does open (and own) the
// default store: rows written through the returned registry are readable, and
// the handle closes without complaint.
func TestRegistryOpenDefault(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())
	reg, err := OpenDefault()
	if err != nil {
		t.Fatalf("OpenDefault: %v", err)
	}
	defer reg.Close()

	rows, err := reg.List(Filter{Limit: 5})
	if err != nil {
		t.Fatalf("List on an empty default store: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none in a fresh store", rows)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close (second): %v", err)
	}
}
