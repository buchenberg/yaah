// subagent_aliases.go re-exports the sub-agent I/O contract and
// context-key helpers from internal/jobs so existing consumers keep
// compiling while the implementation lives in the jobs package.
package tools

import (
	"context"

	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/types"
)

type (
	TaskRunner       = jobs.TaskRunner
	SubAgentParams   = jobs.SubAgentParams
	Escalation       = jobs.Escalation
	TurnRestoreStats = jobs.TurnRestoreStats
)

const (
	EscalationBlocker = jobs.EscalationBlocker
)

// ErrStuckChild stays a variable alias so errors.Is identity is preserved.
var ErrStuckChild = jobs.ErrStuckChild

func ParseSubAgentOutput(output string, runErr error) *jobs.SubAgentOutput {
	return jobs.ParseSubAgentOutput(output, runErr)
}

func SubAgentModelFromContext(ctx context.Context) string {
	return jobs.SubAgentModelFromContext(ctx)
}

func WithSubAgentModelPtr(ctx context.Context, ptr *string) context.Context {
	return jobs.WithSubAgentModelPtr(ctx, ptr)
}

func WriteSubAgentModel(ctx context.Context, model string) {
	jobs.WriteSubAgentModel(ctx, model)
}

func WithSubAgentStartNotifier(ctx context.Context, fn func(model string)) context.Context {
	return jobs.WithSubAgentStartNotifier(ctx, fn)
}

func NotifySubAgentStart(ctx context.Context, model string) {
	jobs.NotifySubAgentStart(ctx, model)
}

func WithSubAgentUsage(ctx context.Context, usage *types.Usage) context.Context {
	return jobs.WithSubAgentUsage(ctx, usage)
}

func AddSubAgentUsage(ctx context.Context, delta types.Usage) {
	jobs.AddSubAgentUsage(ctx, delta)
}

func WithSubAgentHeartbeat(ctx context.Context, hb chan struct{}) context.Context {
	return jobs.WithSubAgentHeartbeat(ctx, hb)
}

func SendHeartbeat(ctx context.Context) {
	jobs.SendHeartbeat(ctx)
}

func WithTurnRestoreStats(ctx context.Context, stats *jobs.TurnRestoreStats) context.Context {
	return jobs.WithTurnRestoreStats(ctx, stats)
}

func RecordTurnRestore(ctx context.Context, restoredFrom string) {
	jobs.RecordTurnRestore(ctx, restoredFrom)
}

func WithConversationCapture(ctx context.Context, ptr *[]types.Message) context.Context {
	return jobs.WithConversationCapture(ctx, ptr)
}

func WriteConversationCapture(ctx context.Context, msgs []types.Message) bool {
	return jobs.WriteConversationCapture(ctx, msgs)
}
