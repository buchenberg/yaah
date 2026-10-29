package tools

import (
	"context"
	"testing"

	"github.com/buchenberg/yaah/internal/jobs"
)

// TestDelegateSharedLaneRecordsWorld pins the per-dispatch world for a
// dispatch that never reaches the retained resolver: it runs on the session
// tree, so the start event must say "shared" rather than staying empty.
func TestDelegateSharedLaneRecordsWorld(t *testing.T) {
	var got SubAgentWorld
	ctx := WithSubAgentWorldPtr(context.Background(), &got)
	d := &DelegateTool{
		Runner: func(ctx context.Context, prompt string, p jobs.SubAgentParams) (string, error) {
			// The runner must see the world already published: a
			// start-notifier fired from here would carry it.
			return "ok", nil
		},
		LaneForRole: func(role string) DelegateLane { return LaneShared },
		// A retained runtime exists but the lane bypasses it.
		Retained: &RetainedRuntime{},
	}
	if _, err := d.Execute(ctx, `{"role":"developer","prompt":"p"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.Resolved != "shared" {
		t.Fatalf("world = %+v, want shared", got)
	}
}

// TestDelegateSharedModeRecordsWorld covers the direct lane when no retained
// runtime exists at all (agents.subagent.mode: shared).
func TestDelegateSharedModeRecordsWorld(t *testing.T) {
	var got SubAgentWorld
	ctx := WithSubAgentWorldPtr(context.Background(), &got)
	d := &DelegateTool{
		Runner: func(ctx context.Context, prompt string, p jobs.SubAgentParams) (string, error) {
			return "ok", nil
		},
	}
	if _, err := d.Execute(ctx, `{"role":"developer","prompt":"p"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.Resolved != "shared" {
		t.Fatalf("world = %+v, want shared", got)
	}
}
