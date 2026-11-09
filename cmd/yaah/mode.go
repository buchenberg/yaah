// Persona vocabulary: the orchestrator persona is the only session axis
// (yaah-21uk.12 retired the plan/build mode seam). The tool gate each
// persona enforces and the system-prompt directive that tells the model which
// regime it is in are generated from the same derivation, so a directive can
// never promise a regime the loop does not actually enforce. The plan/build
// words survive only in the ACP spec surface: session/set_mode and the
// config-option selector map onto the personas of the same name.
package yaah

import (
	"strings"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/prompts"
)

const (
	acpModeBuild = "build"
	acpModePlan  = "plan"
)

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

// effectiveToolsLevel is a test seam over personaLevel (the loop build
// and modeDirective sample the persona once and call personaLevel
// directly); it exists so tests can assert the session-state derivation
// without hand-building a loop.
func (s *agentSession) effectiveToolsLevel() agent.ToolsLevel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return personaLevel(s.persona)
}

// Mode reports the ACP mode vocabulary value the session's persona maps to:
// the plan persona is "plan", everything else "build" (including a hand-built
// zero persona and read-only registry personas, whose narrowing shows up in
// the tools and the directive, not the mode word). The session never STORES
// a mode — the persona column is the truth — so this is a derivation, and
// the session/set_mode and config-option surfaces keep reporting a mode
// clients understand.
func (s *agentSession) Mode() string {
	if s.PersonaName() == subagent.PlanPersonaName {
		return acpModePlan
	}
	return acpModeBuild
}

// modeDirective returns the system prompt for the session's persona:
// mainPrompt with one persona directive injected after the identity block.
// Every persona carries a directive so the model can tell which regime it is
// in, who switches it, and that a switch is announced in-band — the absence
// of a plan directive is no longer the only cue that the full regime is
// active.
func (s *agentSession) modeDirective(persona subagent.Persona) string {
	// Sample the prompt inputs under the session lock: SetPersona swaps
	// mainPrompt/identityEnd/persona atomically, and the directive must
	// never pair a stale prompt with the new persona (or vice versa).
	s.mu.RLock()
	main, end := s.mainPrompt, s.identityEnd
	s.mu.RUnlock()
	// Level from the same derivation the loop's gate uses, so the directive
	// cannot describe a regime this session does not enforce.
	level := personaLevel(persona)
	return prompts.InjectAfterIdentityAt(main, end, []string{
		personaLabel(persona.Name) + " persona is active: " + regimeSentence(level),
		"Only the user switches agents: /agent <name> in the TUI and REPL, the web palette, or ACP session/set_agent or session/set_mode. You may ask for a switch with the question tool or in your reply.",
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
