// output.go defines the structured I/O contract between a
// sub-agent and its caller: the per-invocation params, the escalation
// system, and the parsed output wrapper.
package jobs

import (
	"encoding/json"
	"regexp"

	"github.com/buchenberg/yaah/internal/types"
)

// Seed modes for SubAgentParams.SeedMode (retained-result threading).
const (
	// SeedModeWorkspace seeds a dispatch's world from a retained proposal's
	// sealed workspace state, without its conversation. Safe across roles.
	SeedModeWorkspace = "workspace"
	// SeedModeConversation seeds the workspace and the captured conversation;
	// same-role only.
	SeedModeConversation = "conversation"
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

	// SeedMessages, when non-empty, pre-populates the sub-agent loop's
	// conversation so a continuation resumes from the prior run's context
	// instead of starting fresh. The continuation prompt is still appended as
	// the next user message by the loop's normal message initialization.
	SeedMessages []types.Message

	// SeedFrom, when non-empty, names a retained proposal whose sealed
	// workspace state seeds this dispatch's world. The source proposal stays
	// open (threading reads it); this dispatch runs its own role, prompt, and
	// toolset. Worktree lane only.
	SeedFrom string

	// SeedMode selects what SeedFrom carries: SeedModeWorkspace (default) is
	// the sealed worktree state; SeedModeConversation also seeds the captured
	// messages and is same-role only (a different role's system prompt would
	// otherwise be carried into the new loop).
	SeedMode string

	// JSONMode enables structured JSON output for this sub-agent.
	JSONMode bool

	// Provider and Model are the per-call routing override (parity W1.3):
	// the top of the dispatch resolution chain — call > config role entry
	// > role file > parent session model > config default. Empty means
	// the call did not override.
	Provider string
	Model    string

	// OutputLimit caps the sub-agent's final synthesized result in bytes.
	// 0 means use the role/config default.
	OutputLimit int

	// Workdir, when non-empty, is the directory this sub-agent's file tools and
	// shell commands operate in, and the root its path containment is checked
	// against. The retained lane sets it to the sub-agent's own worktree so the
	// dispatch cannot see or mutate the parent's tree. Empty means the session
	// workspace and process cwd (the default behaviour).
	Workdir string

	// Lane names the dispatch lane for trace aggregation: "shared" or
	// "retained". Empty means unknown (older callers/tests).
	Lane string
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
