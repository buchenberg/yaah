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
