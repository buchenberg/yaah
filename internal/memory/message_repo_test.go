package memory

import (
	"testing"
)

// TestDB_DeleteMessagesFrom pins the range-delete used by compaction
// rebasing and turn restore (review finding B7): only rows at idx >=
// fromIdx of the named session are removed, other sessions untouched.
func TestDB_DeleteMessagesFrom(t *testing.T) {
	db := openTestDB(t)

	for i := 0; i < 5; i++ {
		if err := db.AddMessage(msg(idAt(i), "user", i)); err != nil {
			t.Fatalf("AddMessage idx %d: %v", i, err)
		}
	}
	// A second session whose rows must survive.
	if err := db.CreateSession(Session{ID: "other", StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddMessage(Message{ID: "other-0", SessionID: "other", Idx: 0, Role: "user", Content: "keep", Timestamp: 1}); err != nil {
		t.Fatal(err)
	}

	if err := db.DeleteMessagesFrom("ses-test", 2); err != nil {
		t.Fatalf("DeleteMessagesFrom: %v", err)
	}

	got, err := db.GetMessages("ses-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ses-test rows = %d; want 2 (idx 0,1)", len(got))
	}
	for i, m := range got {
		if m.Idx != i {
			t.Errorf("row %d has idx %d", i, m.Idx)
		}
	}

	other, err := db.GetMessages("other")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 1 {
		t.Errorf("other session rows = %d; want 1", len(other))
	}
}

func idAt(i int) string {
	return string(rune('a'+i)) + "-msg"
}
