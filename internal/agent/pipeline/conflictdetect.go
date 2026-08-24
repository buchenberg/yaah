package pipeline

import (
	"context"
	"strings"

	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// ConflictDetectMiddleware inspects tool results after each batch and
// surfaces parallel-edit file conflicts reported by the tracker. The
// tracker is owned by the composition site; hook emission, OTel span
// decoration, persistence, and state sync are injected callbacks so the
// pipeline stays free of agent-package dependencies.
//
// Appending to step.Messages in PostTool is safe: nothing overwrites
// step.Messages after RunPostTool — the loop adopts it as the new
// conversation tail at the end of the iteration.
type ConflictDetectMiddleware struct {
	tracker *tools.ConflictTracker
	// onCheck fires once per PostTool when a tracker is present (even
	// when no report is produced), mirroring the original inline check.
	onCheck func(ctx context.Context, model string, turn int)
	// onFound fires when DetectAndReset produced a conflict report.
	onFound func(ctx context.Context, model string, turn int, report string, fileCount int)
	// persistMsg persists an appended message (may be nil).
	persistMsg func(msg types.Message)
}

func NewConflictDetectMiddleware(
	tracker *tools.ConflictTracker,
	onCheck func(ctx context.Context, model string, turn int),
	onFound func(ctx context.Context, model string, turn int, report string, fileCount int),
	persistMsg func(msg types.Message),
) *ConflictDetectMiddleware {
	return &ConflictDetectMiddleware{tracker: tracker, onCheck: onCheck, onFound: onFound, persistMsg: persistMsg}
}

func (m *ConflictDetectMiddleware) Name() string { return "conflict_detect" }

func (m *ConflictDetectMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	return step, nil
}

func (m *ConflictDetectMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	return step, nil
}

func (m *ConflictDetectMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	if m.tracker == nil {
		return step, nil
	}
	if m.onCheck != nil {
		m.onCheck(ctx, step.Model, step.Iteration)
	}
	report := m.tracker.DetectAndReset()
	if report == "" {
		return step, nil
	}
	fileCount := strings.Count(report, "File: ")
	if m.onFound != nil {
		m.onFound(ctx, step.Model, step.Iteration, report, fileCount)
	}
	conflictMsg := types.UserMsg(report)
	step.Messages = append(step.Messages, conflictMsg)
	if m.persistMsg != nil {
		m.persistMsg(conflictMsg)
	}
	return step, nil
}
