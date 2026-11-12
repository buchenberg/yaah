package memory

import (
	"errors"
	"path/filepath"
	"testing"
)

// openTestStore opens an isolated store on a temp file for one test.
func openTestStore(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestSavedPermissionSaveLoadDelete covers the store's CRUD shape: a save is
// readable back as one row, re-saving the same triple is a no-op (the unique
// index), a delete removes it, and deleting an unknown row reports the miss
// instead of succeeding silently.
func TestSavedPermissionSaveLoadDelete(t *testing.T) {
	db := openTestStore(t)

	if err := db.SaveSavedPermission(SavedPermission{
		ProjectID: "/repo", Action: "bash", Resource: "git status*",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Re-save a decision the user already made: one row, not two.
	if err := db.SaveSavedPermission(SavedPermission{
		ProjectID: "/repo", Action: "bash", Resource: "git status*",
	}); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	rows, err := db.SavedPermissions("/repo")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].Action != "bash" || rows[0].Resource != "git status*" {
		t.Fatalf("rows = %+v, want one (bash, git status*)", rows)
	}

	// A different project never sees another project's rows.
	other, err := db.SavedPermissions("/other-repo")
	if err != nil {
		t.Fatalf("list /other-repo: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("project scope leaked: /other-repo saw %+v", other)
	}

	if err := db.DeleteSavedPermission("/repo", "bash", "git status*"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if rows, _ := db.SavedPermissions("/repo"); len(rows) != 0 {
		t.Fatalf("rows after delete = %+v, want none", rows)
	}
	if err := db.DeleteSavedPermission("/repo", "bash", "git status*"); !errors.Is(err, ErrSavedPermissionNotFound) {
		t.Fatalf("delete unknown = %v, want ErrSavedPermissionNotFound", err)
	}
}

// TestSavedPermissionRejectsEmptyKeys pins the store's input contract: an
// empty project or action is a caller bug, not a row — the write fails rather
// than persisting an unmatchable triple.
func TestSavedPermissionRejectsEmptyKeys(t *testing.T) {
	db := openTestStore(t)
	if err := db.SaveSavedPermission(SavedPermission{Action: "bash"}); err == nil {
		t.Fatal("empty project accepted")
	}
	if err := db.SaveSavedPermission(SavedPermission{ProjectID: "/repo"}); err == nil {
		t.Fatal("empty action accepted")
	}
}
