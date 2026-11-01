// Mode vocabulary: the plan/build modes, the tool gate each mode enforces,
// and the system-prompt directive that tells the model which regime it is in.
//
// The tool gate and the directive wording are generated from the same spec
// (planModeSpec) so a directive can never promise a regime the loop does not
// actually enforce.
package yaah

import (
	"strings"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/prompts"
)

// PlanModeSpec is the single source of truth for plan mode: its mode name and
// the tool level it enforces. toolsLevelForMode gates on it and modeDirective
// describes it (via regimeSentence), so the two cannot drift.
type PlanModeSpec struct {
	Mode       string
	ToolsLevel agent.ToolsLevel
}

// planModeSpec is the one place plan mode is defined. Changing the tool level
// here changes both the surface the loop advertises and what the directive
// says the model can do.
var planModeSpec = PlanModeSpec{
	Mode:       acpModePlan,
	ToolsLevel: agent.ReadOnlyTools,
}

// toolsLevelForMode maps a session mode to the tool gate the loop enforces.
// Only plan mode narrows the surface; every other mode — including the empty
// default — gets the full toolset.
func (s *agentSession) toolsLevelForMode(mode string) agent.ToolsLevel {
	if mode == planModeSpec.Mode {
		return planModeSpec.ToolsLevel
	}
	return agent.FullTools
}

// modeDirective returns the system prompt for mode: mainPrompt with one mode
// directive injected after the identity block. Every mode carries a directive
// so the model can tell which regime it is in, who switches it, and that a
// switch is announced in-band — the absence of a plan directive is no longer
// the only cue that build mode is active.
func (s *agentSession) modeDirective(mode string) string {
	level := agent.FullTools
	label := modeLabel(acpModeBuild)
	if mode == planModeSpec.Mode {
		level = planModeSpec.ToolsLevel
		label = modeLabel(planModeSpec.Mode)
	}
	return prompts.InjectAfterIdentity(s.mainPrompt, []string{
		label + " mode is active: " + regimeSentence(level),
		"Only the user switches modes: /plan and /build in the TUI and REPL, the web palette, or ACP session/set_mode. You may ask for a switch with the question tool or in your reply.",
		"A mode-change notice appears in-band in the conversation when the mode switches.",
	})
}

// regimeSentence states what the tool gate actually enforces at level. It is
// generated from the level, never written per mode, so the directive cannot
// describe a regime the loop does not implement.
func regimeSentence(level agent.ToolsLevel) string {
	if level == agent.ReadOnlyTools {
		return "mutating tools are withheld from your toolset; only the inspection set is available."
	}
	return "the full toolset is available, but do not mutate the workspace or delegate mutating work without approval."
}

// modeLabel renders a mode name as the directive's opening word. The empty
// mode is build mode.
func modeLabel(mode string) string {
	if mode == "" {
		mode = acpModeBuild
	}
	return strings.ToUpper(mode[:1]) + mode[1:]
}
