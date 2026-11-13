package context

import (
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestActiveTaskPromptSkipsSyntheticUserRows(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "read every file and write the report"},
		{Role: "assistant", Content: "working"},
		{Role: "user", Content: "[STEER] You have called read heavily. Switch approach."},
	}
	if got := ActiveTaskPrompt(msgs); got != "read every file and write the report" {
		t.Errorf("ActiveTaskPrompt = %q, want the genuine task (steer rows are not the task)", got)
	}
	allSynthetic := []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "[STEER] nudge"},
		{Role: "user", Content: "SUPERVISOR: recover"},
	}
	if got := ActiveTaskPrompt(allSynthetic); got != "" {
		t.Errorf("ActiveTaskPrompt = %q, want empty when every user row is synthetic", got)
	}
}
