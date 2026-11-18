package yaah

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/memory"
)

// TestRestoreMessagesCapsAssistantRows pins the replay half of the content cap
// (yaah-hge6).
//
// A session poisoned before the cap existed keeps its oversized row, and a
// restored transcript is the next prompt: without the cap on the way in, every
// resume of that session re-sends the whole loop for the rest of its life.
func TestRestoreMessagesCapsAssistantRows(t *testing.T) {
	huge := strings.Repeat("GO ", 400000) // 1.2 MB, the shape of the real row

	rows := []memory.Message{
		{Role: "system", Content: "identity", Idx: 0},
		{Role: "user", Content: "what is the plan?", Idx: 1},
		{Role: "assistant", Content: huge, Idx: 2},
	}

	got := restoreMessages(rows)
	if len(got) != 3 {
		t.Fatalf("restored %d messages; want 3", len(got))
	}
	if len(got[2].Content) > 256<<10 {
		t.Errorf("restored assistant content = %d bytes; want <= %d", len(got[2].Content), 256<<10)
	}
	if !strings.Contains(got[2].Content, "assistant message truncated") {
		t.Error("the restored row carries no truncation marker")
	}
	// Only the assistant row is capped: the user's prompt is what the user
	// wrote, and it is not this guard's business to edit it.
	if got[1].Content != "what is the plan?" {
		t.Errorf("user content = %q; want it untouched", got[1].Content)
	}
	if got[0].Content != "identity" {
		t.Errorf("system content = %q; want it untouched", got[0].Content)
	}
}
