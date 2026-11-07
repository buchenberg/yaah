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

// validMode reports whether mode is one a session can hold. Build and plan are
// the only two; the empty string is the zero value that MEANS build, not a mode
// a caller may select, so it is refused rather than normalized here.
func validMode(mode string) bool {
	return mode == acpModeBuild || mode == planModeSpec.Mode
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
	// Level from the same derivation the loop's gate uses, so the directive
	// cannot describe a regime this session does not enforce.
	level := s.toolsLevelForMode(mode)
	label := modeLabel(mode)
	return prompts.InjectAfterIdentityAt(s.mainPrompt, s.identityEnd, []string{
		label + " mode is active: " + regimeSentence(level),
		"Only the user switches modes: /plan and /build in the TUI and REPL, the web palette, or ACP session/set_mode. You may ask for a switch with the question tool or in your reply.",
		"A mode-change notice appears in-band in the conversation when the mode switches.",
	})
}

// modeChangeNotice renders the one-line in-band notice announcing a switch to
// mode. It names the new mode and the regime that mode enforces, and takes the
// regime sentence from regimeSentence so the notice and the directive can never
// describe different tool surfaces. The tool level comes from
// toolsLevelForMode — the same derivation the loop's gate uses — rather than a
// second copy of the plan-mode condition.
func (s *agentSession) modeChangeNotice(mode string) string {
	return "Mode changed to " + modeLabel(mode) + " — " + regimeSentence(s.toolsLevelForMode(mode))
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
	mode = modeOrBuild(mode)
	return strings.ToUpper(mode[:1]) + mode[1:]
}

// modeOrBuild resolves a stored mode's zero value to the mode it means: the
// empty string is build's storage form, not a third mode.
func modeOrBuild(mode string) string {
	if mode == "" {
		return acpModeBuild
	}
	return mode
}

// sameMode reports whether two mode values denote the same regime. A mode is a
// selection, and the empty storage form and build are one selection, so
// switching a session that never switched to build is not a change.
func sameMode(a, b string) bool {
	return modeOrBuild(a) == modeOrBuild(b)
}
