package memory

import (
	"path/filepath"
	"testing"
)

func TestSessionWatchesRoundTripAndOwnership(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	a := SessionWatch{
		ID: "w1", On: "time:5m", Until: "", State: "armed",
		ArmedAt: 100, ExpiresAt: 200, IntervalMs: 1000,
	}
	b := SessionWatch{
		ID: "w1", On: "file:ci/green", Until: "exists", State: "fired",
		Evidence: "matched", ArmedAt: 300, ExpiresAt: 400, FiredAt: 350,
	}
	if err := db.SaveSessionWatch("sess-a", a); err != nil {
		t.Fatalf("SaveSessionWatch(a): %v", err)
	}
	// Same id in a different session must not collide: watches are per
	// session, and a resume must not see another session's armed set.
	if err := db.SaveSessionWatch("sess-b", b); err != nil {
		t.Fatalf("SaveSessionWatch(b): %v", err)
	}

	rows, err := db.ListSessionWatches("sess-a")
	if err != nil {
		t.Fatalf("ListSessionWatches: %v", err)
	}
	if len(rows) != 1 || rows[0] != a {
		t.Fatalf("sess-a rows = %+v, want %+v", rows, a)
	}

	// Update in place (fire transition) rather than inserting a duplicate.
	a.State = "fired"
	a.Evidence = "fired"
	if err := db.SaveSessionWatch("sess-a", a); err != nil {
		t.Fatalf("SaveSessionWatch(update): %v", err)
	}
	rows, err = db.ListSessionWatches("sess-a")
	if err != nil {
		t.Fatalf("ListSessionWatches after update: %v", err)
	}
	if len(rows) != 1 || rows[0].State != "fired" || rows[0].Evidence != "fired" {
		t.Fatalf("updated rows = %+v", rows)
	}

	if err := db.DeleteSessionWatch("sess-a", "w1"); err != nil {
		t.Fatalf("DeleteSessionWatch: %v", err)
	}
	if n, err := db.countSessionWatches("sess-a"); err != nil || n != 0 {
		t.Fatalf("sess-a count after delete = %d, err=%v", n, err)
	}
	if n, err := db.countSessionWatches("sess-b"); err != nil || n != 1 {
		t.Fatalf("sess-b count after deleting sess-a = %d, err=%v; want 1", n, err)
	}

	if err := db.DeleteSessionWatches("sess-b"); err != nil {
		t.Fatalf("DeleteSessionWatches: %v", err)
	}
	if n, err := db.countSessionWatches("sess-b"); err != nil || n != 0 {
		t.Fatalf("sess-b count after bulk delete = %d, err=%v", n, err)
	}
}
