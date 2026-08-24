package pipeline

import (
	"context"
	"testing"

	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

func TestConflictDetect_NilTrackerIsNoop(t *testing.T) {
	m := NewConflictDetectMiddleware(nil, nil, nil, nil)
	step := &Step{Messages: []types.Message{}}
	if _, err := m.PostTool(context.Background(), []ToolResult{}, step); err != nil {
		t.Fatal(err)
	}
	if len(step.Messages) != 0 {
		t.Error("nil tracker should not modify step")
	}
}

func TestConflictDetect_NoReportEmitsCheckOnly(t *testing.T) {
	tracker := &tools.ConflictTracker{}
	checks, found := 0, 0
	m := NewConflictDetectMiddleware(tracker,
		func(ctx context.Context, model string, turn int) { checks++ },
		func(ctx context.Context, model string, turn int, report string, fileCount int) { found++ },
		nil,
	)
	step := &Step{Messages: []types.Message{}, Model: "m", Iteration: 2}
	if _, err := m.PostTool(context.Background(), []ToolResult{}, step); err != nil {
		t.Fatal(err)
	}
	if checks != 1 {
		t.Errorf("onCheck fired %d times, want 1", checks)
	}
	if found != 0 {
		t.Errorf("onFound fired %d times without a report", found)
	}
}

func TestConflictDetect_ReportAppendsMessage(t *testing.T) {
	tracker := &tools.ConflictTracker{}
	tracker.Record("worker-a", "shared.go", "write")
	tracker.Record("worker-b", "shared.go", "edit")
	tracker.Record("worker-a", "other.go", "write")
	tracker.Record("worker-b", "other.go", "edit")
	var persisted types.Message
	var gotReport string
	var gotFiles int
	m := NewConflictDetectMiddleware(tracker,
		func(ctx context.Context, model string, turn int) {},
		func(ctx context.Context, model string, turn int, report string, fileCount int) {
			gotReport = report
			gotFiles = fileCount
		},
		func(msg types.Message) { persisted = msg },
	)
	step := &Step{Messages: []types.Message{}, Model: "m", Iteration: 1}
	res, err := m.PostTool(context.Background(), []ToolResult{}, step)
	if err != nil {
		t.Fatal(err)
	}
	if res != step || len(step.Messages) != 1 {
		t.Fatalf("expected conflict message appended, got %d messages", len(step.Messages))
	}
	if step.Messages[0].Role != "user" || step.Messages[0].Content == "" {
		t.Errorf("unexpected conflict message: %+v", step.Messages[0])
	}
	if gotReport == "" {
		t.Error("onFound did not receive the report")
	}
	if gotFiles != 2 {
		t.Errorf("fileCount = %d, want 2", gotFiles)
	}
	if persisted.Content != step.Messages[0].Content {
		t.Error("persist callback did not receive the appended message")
	}
	// Tracker resets after detection: second PostTool finds nothing.
	if r := tracker.DetectAndReset(); r != "" {
		t.Errorf("tracker not reset, leftover report: %q", r)
	}
}
