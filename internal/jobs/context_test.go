package jobs

import (
	"context"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestTurnRestoreStats_RecordedThroughContext(t *testing.T) {
	var stats TurnRestoreStats
	ctx := WithTurnRestoreStats(context.Background(), &stats)

	RecordTurnRestore(ctx, "cp-1")
	RecordTurnRestore(ctx, "cp-2")

	if stats.Restores != 2 {
		t.Errorf("Restores = %d, want 2", stats.Restores)
	}
	if stats.RestoredFrom != "cp-2" {
		t.Errorf("RestoredFrom = %q, want cp-2 (last restore wins)", stats.RestoredFrom)
	}
}

func TestTurnRestoreStats_NoopWithoutContextValue(t *testing.T) {
	// Must not panic when no stats pointer was stored.
	RecordTurnRestore(context.Background(), "cp-1")
}

func TestConversationCapture_WriteThroughContext(t *testing.T) {
	var captured []types.Message
	ctx := WithConversationCapture(context.Background(), &captured)

	msgs := []types.Message{types.SystemMsg("sp"), types.UserMsg("work")}
	if ok := WriteConversationCapture(ctx, msgs); !ok {
		t.Fatal("WriteConversationCapture = false, want true with pointer stored")
	}
	if len(captured) != 2 || captured[1].Content != "work" {
		t.Errorf("captured = %v, want seeded msgs", captured)
	}
}

func TestConversationCapture_NoopWithoutContextValue(t *testing.T) {
	// Must not panic and must report false when no pointer was stored.
	if ok := WriteConversationCapture(context.Background(), nil); ok {
		t.Error("WriteConversationCapture = true, want false without pointer")
	}
}
