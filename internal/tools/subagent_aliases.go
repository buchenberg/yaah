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

// ErrDispatchStartup is the dispatch-startup counterpart of ErrStuckChild:
// the world was provisioned but the child never heartbeat, so the dispatch
// never started rather than hanging mid-run.
var ErrDispatchStartup = jobs.ErrDispatchStartup

// ErrScopeExhausted is a variable alias of jobs.ErrScopeExhausted (same
// rule as above: variable alias so errors.Is identity is preserved).
var ErrScopeExhausted = jobs.ErrScopeExhausted

func ParseSubAgentOutput(output string, runErr error) *jobs.SubAgentOutput {
	return jobs.ParseSubAgentOutput(output, runErr)
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

func WriteSubAgentWorld(ctx context.Context, w SubAgentWorld) {
	jobs.WriteSubAgentWorld(ctx, w)
}

func WithSubAgentUsage(ctx context.Context, usage *types.Usage) context.Context {
	return jobs.WithSubAgentUsage(ctx, usage)
}

func AddSubAgentUsage(ctx context.Context, delta types.Usage) {
	jobs.AddSubAgentUsage(ctx, delta)
}

// CaptureSubAgentTranscript reports the sub-agent's final conversation through
// the transcript sink in ctx, if one was installed.
func CaptureSubAgentTranscript(ctx context.Context, messages []types.Message) {
	jobs.CaptureSubAgentTranscript(ctx, messages)
}

// SubAgentStop is the runner's typed stop outcome (aliased from jobs).
type SubAgentStop = jobs.SubAgentStop

// CaptureSubAgentStop reports the run's typed stop outcome through the sink in
// ctx, if one was installed.
func CaptureSubAgentStop(ctx context.Context, stop SubAgentStop) {
	jobs.CaptureSubAgentStop(ctx, stop)
}

func WithSubAgentReadyNotifier(ctx context.Context, fn func()) context.Context {
	return jobs.WithSubAgentReadyNotifier(ctx, fn)
}

func NotifySubAgentReady(ctx context.Context) {
	jobs.NotifySubAgentReady(ctx)
}

func WithSubAgentHeartbeat(ctx context.Context, hb chan struct{}) context.Context {
	return jobs.WithSubAgentHeartbeat(ctx, hb)
}

func SendHeartbeat(ctx context.Context) {
	jobs.SendHeartbeat(ctx)
}
