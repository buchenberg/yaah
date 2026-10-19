package yaah

import (
	"testing"

	"github.com/buchenberg/yaah/internal/memory"
)

// TestRestoreMessage_carriesReasoningSignature pins that resume does not drop
// the thinking signature. A thinking block is only re-emitted when both its
// content and its signature are present, so losing the signature here makes
// replay silently stop working after `yaah resume` even though the column
// was loaded from the DB.
func TestRestoreMessage_carriesReasoningSignature(t *testing.T) {
	got := restoreMessage(memory.Message{
		Role:               "assistant",
		Content:            "answer",
		ReasoningContent:   "because",
		ReasoningSignature: "sig-1",
		ToolName:           "bash",
		ToolCallID:         "call-1",
	})

	if got.ReasoningContent != "because" {
		t.Errorf("ReasoningContent = %q", got.ReasoningContent)
	}
	if got.ReasoningSignature != "sig-1" {
		t.Errorf("ReasoningSignature = %q, want %q", got.ReasoningSignature, "sig-1")
	}
	if got.Name != "bash" || got.ToolCallID != "call-1" {
		t.Errorf("tool identity lost: name=%q id=%q", got.Name, got.ToolCallID)
	}
}
