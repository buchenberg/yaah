package yaah

import (
	"path/filepath"
	"testing"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/wake"
)

func TestWakeStoreRoundTripsEvidence(t *testing.T) {
	db, err := memory.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	store := wakeStore{db: db}
	code := 0
	rec := wake.WatchRecord{
		ID: "w1", On: "command:make", State: wake.StateFired,
		Evidence: wake.Evidence{
			Kind: "command", ExitCode: &code, Excerpt: "ok\n", Detail: "probe",
		},
		ArmedAt: 1, ExpiresAt: 2, FiredAt: 3, IntervalMs: 4,
		Owner: "worker", LeaseUntil: 5,
	}
	if err := store.SaveWatch("sess", rec); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	rows, err := store.ListWatches("sess")
	if err != nil {
		t.Fatalf("ListWatches: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	ev := rows[0].Evidence
	if ev.Kind != "command" || ev.ExitCode == nil || *ev.ExitCode != 0 || ev.Excerpt != "ok\n" {
		t.Fatalf("round-tripped evidence = %+v", ev)
	}
	if rows[0].Owner != "worker" || rows[0].LeaseUntil != 5 {
		t.Fatalf("round-tripped ownership = owner %q lease %d", rows[0].Owner, rows[0].LeaseUntil)
	}

	// A prose-era row (the column was plain text before the envelope) is
	// still readable: it lands in Detail.
	if err := db.SaveSessionWatch("sess", memory.SessionWatch{
		ID: "w2", On: "file:x", State: wake.StateFired,
		Evidence: "job bg-1 completed with a report", ArmedAt: 1,
	}); err != nil {
		t.Fatalf("SaveSessionWatch(legacy): %v", err)
	}
	rows, err = store.ListWatches("sess")
	if err != nil {
		t.Fatalf("ListWatches(legacy): %v", err)
	}
	found := false
	for _, row := range rows {
		if row.ID == "w2" {
			found = true
			if row.Evidence.Detail != "job bg-1 completed with a report" || row.Evidence.Kind != "" {
				t.Fatalf("legacy evidence = %+v", row.Evidence)
			}
		}
	}
	if !found {
		t.Fatal("legacy watch row was not returned")
	}
}
