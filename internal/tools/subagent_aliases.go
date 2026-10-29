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
	TaskRunner     = jobs.TaskRunner
	SubAgentParams = jobs.SubAgentParams
	Escalation     = jobs.Escalation
	SubAgentWorld  = jobs.SubAgentWorld
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

func WithSubAgentWorldPtr(ctx context.Context, ptr *SubAgentWorld) context.Context {
	return jobs.WithSubAgentWorldPtr(ctx, ptr)
}

func WithSubAgentWorldWriter(ctx context.Context, write func(SubAgentWorld)) context.Context {
	return jobs.WithSubAgentWorldWriter(ctx, write)
}

func WriteSubAgentWorld(ctx context.Context, w SubAgentWorld) {
	jobs.WriteSubAgentWorld(ctx, w)
}

func SubAgentWorldFromContext(ctx context.Context) SubAgentWorld {
	return jobs.SubAgentWorldFromContext(ctx)
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
