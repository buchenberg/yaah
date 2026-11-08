// Persona vocabulary: the orchestrator persona is the only session axis
// (yaah-21uk.12 part 2 retired the plan/build mode seam). The tool gate each
// persona enforces and the system-prompt directive that tells the model which
// regime it is in are generated from the same derivation, so a directive can
// never promise a regime the loop does not actually enforce. The plan/build
// MODE names survive only as deprecated aliases: surfaces accept them and
// SetMode maps them onto the personas of the same name.
package yaah

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/prompts"
)

const (
	acpModeBuild = "build"
	acpModePlan  = "plan"
)

// modeDeprecationNote is the retirement wording every mode-alias surface
// (REPL /plan //build, the TUI palette, the web palette, ACP
// session/set_mode) appends to its confirmation. The aliases route through
// SetMode and stay for one more release; /agent is the switch.
const modeDeprecationNote = "(deprecated: mode switching is retiring — use /agent <name>; the mode aliases remain for one more release)"

// personaLevel is the tool gate a session running persona enforces on its
// next loop build: a read-only persona gets the inspection set, everything
// else the full toolset. The plan/build mode names have no say — the plan
// persona IS the read-only persona, so the mode branch folded away with the
// seam.
func personaLevel(persona subagent.Persona) agent.ToolsLevel {
	if persona.ReadOnly {
		return agent.ReadOnlyTools
	}
	return agent.FullTools
}

// sampledToolsLevel is the loop build's pure core over the already-sampled
// persona — the build samples the persona once, so the advertised set, the
// prompt's regime sentence, and the gate agree even if a switch lands
// concurrently with the build.
func sampledToolsLevel(persona subagent.Persona) agent.ToolsLevel {
	return personaLevel(persona)
}

// effectiveToolsLevel is the tool gate this session's next loop build
// enforces, derived from the current persona under the session lock.
// modeDirective derives its wording from this same derivation, so a
// directive can never promise a regime the loop does not enforce.
func (s *agentSession) effectiveToolsLevel() agent.ToolsLevel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return personaLevel(s.persona)
}

// modePersonaName maps a legacy mode name onto the persona of the same name
// — the whole of SetMode's former semantics. The empty string is not a
// selectable mode (it is build's storage form and the zero value, not a
// selection), so it is refused like any other unknown mode.
func modePersonaName(mode string) (string, bool) {
	switch mode {
	case acpModePlan:
		return subagent.PlanPersonaName, true
	case acpModeBuild:
		return subagent.BuildPersonaName, true
	}
	return "", false
}

// Mode reports the ACP mode vocabulary value the session's persona maps to:
// the plan persona is "plan", everything else "build" (including a hand-built
// zero persona and read-only registry personas, whose narrowing shows up in
// the tools and the directive, not the mode word). The session no longer
// STORES a mode — the persona column is the truth — so this is a derivation,
// and the session/set_mode and config-option surfaces keep reporting a mode
// clients understand.
func (s *agentSession) Mode() string {
	if s.PersonaName() == subagent.PlanPersonaName {
		return acpModePlan
	}
	return acpModeBuild
}

// SetMode is the deprecated plan/build compat wrapper: it maps the mode name
// onto the persona of the same name and switches the persona. The mode column
// is no longer written — the persona column is the only persisted axis — and
// the in-band notice carries the regime sentence plus the deprecation note,
// because the alias surfaces (REPL /plan //build, the TUI and web palettes,
// ACP session/set_mode) all route through here. Unknown modes are refused
// with the historic wording.
func (s *agentSession) SetMode(mode string) error {
	name, ok := modePersonaName(mode)
	if !ok {
		return fmt.Errorf("unknown session mode %q (available: %s, %s)", mode, acpModeBuild, acpModePlan)
	}
	return s.setPersonaNoticed(name, " "+modeDeprecationNote)
}

// modeDirective returns the system prompt for the session's persona:
// mainPrompt with one persona directive injected after the identity block.
// Every persona carries a directive so the model can tell which regime it is
// in, who switches it, and that a switch is announced in-band — the absence
// of a plan directive is no longer the only cue that the full regime is
// active.
func (s *agentSession) modeDirective(persona subagent.Persona) string {
	// Level from the same derivation the loop's gate uses, so the directive
	// cannot describe a regime this session does not enforce.
	level := personaLevel(persona)
	return prompts.InjectAfterIdentityAt(s.mainPrompt, s.identityEnd, []string{
		personaLabel(persona.Name) + " persona is active: " + regimeSentence(level),
		"Only the user switches agents: /agent <name> in the TUI and REPL, the web palette, or ACP session/set_agent (session/set_mode still works as a deprecated alias). You may ask for a switch with the question tool or in your reply.",
		"An agent-change notice appears in-band in the conversation when the persona switches.",
	})
}

// regimeSentence states what the tool gate actually enforces at level. It is
// generated from the level, never written per persona, so the directive
// cannot describe a regime the loop does not implement.
func regimeSentence(level agent.ToolsLevel) string {
	if level == agent.ReadOnlyTools {
		return "mutating tools are withheld from your toolset; only the inspection set is available."
	}
	return "the full toolset is available, but do not mutate the workspace or delegate mutating work without approval."
}

// personaLabel renders a persona name as the directive's opening word.
func personaLabel(name string) string {
	if name == "" {
		name = subagent.BuildPersonaName
	}
	return strings.ToUpper(name[:1]) + name[1:]
}
