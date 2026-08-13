package jobs

import (
	"context"
	"testing"
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
