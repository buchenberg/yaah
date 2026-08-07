// subagent_aliases.go re-exports the sub-agent I/O contract and
// context-key helpers from internal/jobs so existing consumers keep
// compiling while the implementation lives in the jobs package.
package tools

import "github.com/buchenberg/yaah/internal/jobs"

type (
	TaskRunner          = jobs.TaskRunner
	SubAgentParams      = jobs.SubAgentParams
	SubAgentOutput      = jobs.SubAgentOutput
	Escalation          = jobs.Escalation
	EscalationSeverity  = jobs.EscalationSeverity
	BackgroundJobs      = jobs.BackgroundJobs
	BackgroundJobStatus = jobs.BackgroundJobStatus
)

const (
	EscalationInfo     = jobs.EscalationInfo
	EscalationWarning  = jobs.EscalationWarning
	EscalationBlocker  = jobs.EscalationBlocker
	EscalationCritical = jobs.EscalationCritical

	BGStatusRunning   = jobs.BGStatusRunning
	BGStatusCompleted = jobs.BGStatusCompleted
	BGStatusFailed    = jobs.BGStatusFailed
	BGStatusCancelled = jobs.BGStatusCancelled
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
	NewBackgroundJobs         = jobs.NewBackgroundJobs
	ErrStuckChild             = jobs.ErrStuckChild
)
