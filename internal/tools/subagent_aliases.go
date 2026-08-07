// subagent_aliases.go re-exports the sub-agent I/O contract and
// context-key helpers from internal/jobs so existing consumers keep
// compiling while the implementation lives in the jobs package.
package tools

import "github.com/buchenberg/yaah/internal/jobs"

type (
	TaskRunner     = jobs.TaskRunner
	SubAgentParams = jobs.SubAgentParams
	Escalation     = jobs.Escalation
)

const (
	EscalationBlocker = jobs.EscalationBlocker
)

var (
	ParseSubAgentOutput       = jobs.ParseSubAgentOutput
	SubAgentModelFromContext  = jobs.SubAgentModelFromContext
	WithSubAgentModelPtr      = jobs.WithSubAgentModelPtr
	WriteSubAgentModel        = jobs.WriteSubAgentModel
	WithSubAgentStartNotifier = jobs.WithSubAgentStartNotifier
	NotifySubAgentStart       = jobs.NotifySubAgentStart
	WithSubAgentUsage         = jobs.WithSubAgentUsage
	AddSubAgentUsage          = jobs.AddSubAgentUsage
	WithSubAgentHeartbeat     = jobs.WithSubAgentHeartbeat
	SendHeartbeat             = jobs.SendHeartbeat
	ErrStuckChild             = jobs.ErrStuckChild
)
