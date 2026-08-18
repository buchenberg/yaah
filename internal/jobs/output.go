// output.go defines the structured I/O contract between a
// sub-agent and its caller: the per-invocation params, the escalation
// system, and the parsed output wrapper.
package jobs

import (
	"encoding/json"
	"regexp"

	"github.com/buchenberg/yaah/internal/types"
)

// SubAgentParams carries the per-invocation sub-agent configuration that
// the model may supply via the task tool arguments. It is passed through
// to the TaskRunner so the runner can build a role-appropriate Loop.
type SubAgentParams struct {
	// Role selects the tool profile and default limits. Empty means the
	// legacy default tool set.
	Role string

	// TimeoutSeconds, when > 0, overrides the role's default timeout.
	TimeoutSeconds int

	// MaxLoopCycles, when > 0, overrides the role's default iteration cap.
	MaxLoopCycles int

	// MaxToolTurns, when > 0, overrides the soft turn cap for tool-using turns.
	MaxToolTurns int

	// JSONMode enables structured JSON output for this sub-agent.
	JSONMode bool

	// OutputLimit caps the sub-agent's final synthesized result in bytes.
	// 0 means use the role/config default.
	OutputLimit int

	// SeedMessages, when non-empty, seeds the sub-agent loop's
	// conversation with prior history so a follow-up dispatch continues
	// from the same point instead of starting fresh. Used by the
	// supervised review session to carry sub-agent context across work
	// units. The prompt is appended as a new user message after the seed.
	SeedMessages []types.Message
}

// EscalationSeverity classifies how serious a sub-agent escalation is.
type EscalationSeverity string

const (
	EscalationInfo     EscalationSeverity = "info"
	EscalationWarning  EscalationSeverity = "warning"
	EscalationBlocker  EscalationSeverity = "blocker"
	EscalationCritical EscalationSeverity = "critical"
)

// Escalation represents a structured issue raised by a sub-agent that
// could not complete its task due to a blocker, error, or caveat.
type Escalation struct {
	Severity   EscalationSeverity `json:"severity"`
	Summary    string             `json:"summary"`
	Detail     string             `json:"detail,omitempty"`
	Suggestion string             `json:"suggestion,omitempty"`
}

// SubAgentOutput is the structured result from a completed sub-agent.
// It wraps the raw output string with any parsed escalation and error.
type SubAgentOutput struct {
	RawOutput  string      `json:"raw_output"`
	Escalation *Escalation `json:"escalation,omitempty"`
	Error      string      `json:"error,omitempty"`
}

var escalationPattern = regexp.MustCompile(
	"(?s)```escalation\\s*\\n(.*?)\\n```",
)

// ParseSubAgentOutput extracts structured escalation data from a sub-agent's
// final output string. Returns the raw output and any parsed escalation.
func ParseSubAgentOutput(output string, runErr error) *SubAgentOutput {
	result := &SubAgentOutput{RawOutput: output}
	if runErr != nil {
		result.Error = runErr.Error()
	}

	m := escalationPattern.FindStringSubmatch(output)
	if len(m) < 2 {
		return result
	}

	var esc Escalation
	if err := json.Unmarshal([]byte(m[1]), &esc); err != nil {
		return result
	}
	result.Escalation = &esc
	return result
}
