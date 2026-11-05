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

func TestSessionWatchClaimAndFire(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	_ = db.SaveSessionWatch("sess", SessionWatch{ID: "w1", On: "time:1h", State: "armed", ArmedAt: 10, Owner: "old"})
	_ = db.SaveSessionWatch("sess", SessionWatch{ID: "w2", On: "time:1h", State: "armed", ArmedAt: 20, Owner: "old"})

	// Last resume wins: the new owner takes over every row.
	rows, err := db.ClaimSessionWatches("sess", "new", 999)
	if err != nil {
		t.Fatalf("ClaimSessionWatches: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("claimed rows = %d, want 2", len(rows))
	}
	for _, row := range rows {
		if row.Owner != "new" || row.LeaseUntil != 999 {
			t.Fatalf("claimed row = %+v", row)
		}
	}

	// The old owner can no longer fire.
	ok, err := db.ClaimSessionWatchFire("sess", "w1", "old", 10, 100, 500, "old evidence")
	if err != nil || ok {
		t.Fatalf("old owner claimed fire: ok=%v err=%v", ok, err)
	}
	// The current owner can.
	ok, err = db.ClaimSessionWatchFire("sess", "w1", "new", 10, 200, 500, "new evidence")
	if err != nil || !ok {
		t.Fatalf("new owner claim: ok=%v err=%v", ok, err)
	}
	// A second fire after the row is fired is refused.
	ok, err = db.ClaimSessionWatchFire("sess", "w1", "new", 10, 300, 500, "again")
	if err != nil || ok {
		t.Fatalf("second fire claim: ok=%v err=%v", ok, err)
	}

	// A later resume claims only the still-armed row; the fired row keeps
	// its owner (a live owner may still be delivering it).
	rows, err = db.ClaimSessionWatches("sess", "third", 777)
	if err != nil {
		t.Fatalf("second ClaimSessionWatches: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "w2" || rows[0].Owner != "third" {
		t.Fatalf("second claim = %+v, want only w2 for third", rows)
	}
	all, err := db.ListSessionWatches("sess")
	if err != nil {
		t.Fatalf("ListSessionWatches: %v", err)
	}
	for _, row := range all {
		if row.ID == "w1" && row.Owner != "new" {
			t.Fatalf("fired row owner changed to %q", row.Owner)
		}
	}

	rows, err = db.ListSessionWatches("sess")
	if err != nil {
		t.Fatalf("ListSessionWatches: %v", err)
	}
	for _, row := range rows {
		if row.ID == "w1" {
			if row.State != "fired" || row.Evidence != "new evidence" || row.FiredAt != 200 {
				t.Fatalf("w1 after fire = %+v", row)
			}
		}
	}
}
