package memory

import (
	"database/sql"
	"errors"
	"testing"
)

// TestSessionModelRoundTrip pins the model-pick column (parity W4): the
// wire form written on a switch reads back on resume, the column starts
// empty (pre-W4 shape), an unknown id surfaces sql.ErrNoRows, and the
// setter rejects a pick without both halves.
func TestSessionModelRoundTrip(t *testing.T) {
	db := openTestDB(t)
	if got, err := db.SessionModel("ses-test"); err != nil || got != "" {
		t.Fatalf("fresh session model = %q, %v; want \"\", nil", got, err)
	}
	if err := db.SetSessionModel("ses-test", "zai", "GLM-5.3"); err != nil {
		t.Fatalf("SetSessionModel: %v", err)
	}
	if got, err := db.SessionModel("ses-test"); err != nil || got != "zai/GLM-5.3" {
		t.Fatalf("stored model = %q, %v; want zai/GLM-5.3, nil", got, err)
	}

	if err := db.SetSessionModel("ses-test", "zai", ""); err == nil {
		t.Error("a pick without a model must be rejected")
	}
	if err := db.SetSessionModel("ses-test", "", "GLM-5.3"); err == nil {
		t.Error("a pick without a provider must be rejected")
	}
	if _, err := db.SessionModel("ses-nonesuch"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unknown id = %v; want sql.ErrNoRows", err)
	}
}

// TestSessionEndIdempotent pins the double-close contract (yaah-cqxg): a
// session's close funnels through EndSession from more than one caller (a
// signal funnel racing the driver's deferred close, a retried teardown), so
// the write must be safe to repeat. ended_at only ever moves forward — a
// second close cannot reopen the row (ended_at back to 0) or pin it to a stale
// clock, and a resumed session's row, which still carries the previous
// process's ended_at, still advances on the next close. The usage columns take
// the maximum, like the incremental flush, so a close carrying a stale or
// zeroed snapshot cannot shrink what the row already reports.
func TestSessionEndIdempotent(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateSession(Session{ID: "sess-close", StartedAt: 1000}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := db.EndSession("sess-close", 2000, 500, 300, 120, 200, 100); err != nil {
		t.Fatalf("EndSession: %v", err)
	}

	t.Run("a later close advances ended_at and keeps the larger usage", func(t *testing.T) {
		// A second close with a later clock but a stale, smaller snapshot:
		// the row must keep the bigger totals and end later, never earlier.
		if err := db.EndSession("sess-close", 3000, 10, 5, 1, 2, 3); err != nil {
			t.Fatalf("second EndSession: %v", err)
		}
		s, err := db.GetSession("sess-close")
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if s.EndedAt != 3000 {
			t.Errorf("ended_at = %d; want 3000 (a later close must advance it)", s.EndedAt)
		}
		if s.TokensIn != 500 || s.TokensOut != 300 || s.TokensCached != 120 {
			t.Errorf("tokens = %d/%d/%d; want 500/300/120 (a close must never shrink usage)",
				s.TokensIn, s.TokensOut, s.TokensCached)
		}
		if s.SubTokensIn != 200 || s.SubTokensOut != 100 {
			t.Errorf("sub tokens = %d/%d; want 200/100", s.SubTokensIn, s.SubTokensOut)
		}
	})

	t.Run("a stale clock cannot move ended_at backwards or to zero", func(t *testing.T) {
		if err := db.EndSession("sess-close", 0, 600, 400, 130, 210, 110); err != nil {
			t.Fatalf("third EndSession: %v", err)
		}
		s, err := db.GetSession("sess-close")
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if s.EndedAt != 3000 {
			t.Errorf("ended_at = %d; want 3000 (a zero clock must not reopen the session)", s.EndedAt)
		}
		// The larger snapshot still lands: the guard is monotonic, not frozen.
		if s.TokensIn != 600 || s.TokensOut != 400 {
			t.Errorf("tokens = %d/%d; want 600/400 (a larger close still advances usage)", s.TokensIn, s.TokensOut)
		}
	})
}

// TestSessionEndUnknownIDIsReported pins the other half of the guard: an end
// write for an id with no row is a no-op at the SQL level, and a session that
// silently keeps ended_at = 0 is exactly the failure yaah-cqxg reports. The
// store must say so instead of returning success.
func TestSessionEndUnknownIDIsReported(t *testing.T) {
	db := openTestDB(t)
	err := db.EndSession("sess-nonesuch", 2000, 1, 1, 0, 0, 0)
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("EndSession(unknown) = %v; want ErrSessionNotFound", err)
	}
}
