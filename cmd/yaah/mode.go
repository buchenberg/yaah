// Session axis vocabulary: the persona is the agent axis and readOnly is
// the mode axis (decouple-persona-mode-approval): two orthogonal session
// axes. The tool gate each session enforces and the system-prompt directive
// that tells the model which regime it is in are generated from the same
// derivation, so a directive can never promise a regime the loop does not
// actually enforce. The plan/build words survive only in the ACP spec
// surface: session/set_mode and the config-option selector map onto the
// mode axis.
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

// sessionToolsLevel is the tool gate a session enforces on its next loop
// build, derived from the session's mode axis: a read-only session gets
// the inspection set, everything else the full toolset. The persona has
// no say — read-only-ness is session state, not a persona property
// (decouple plan Phases 1–2).
func sessionToolsLevel(readOnly bool) agent.ToolsLevel {
	if readOnly {
		return agent.ReadOnlyTools
	}
	return agent.FullTools
}

// effectiveToolsLevel is a test seam over sessionToolsLevel (the loop build
// and modeDirective sample the axes once and call sessionToolsLevel
// directly); it exists so tests can assert the session-state derivation
// without hand-building a loop.
func (s *agentSession) effectiveToolsLevel() agent.ToolsLevel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sessionToolsLevel(s.readOnly)
}

// IsReadOnly reports the session's mode axis: whether this session may
// mutate the workspace. Guarded by mu like every other mutable session
// field.
func (s *agentSession) IsReadOnly() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readOnly
}

// SetReadOnly switches the session's mode axis mid-session (ACP
// session/set_mode and the mode config option). Effective at the NEXT loop
// build, like every other axis switch: an in-flight run keeps the mode it
// started with.
func (s *agentSession) SetReadOnly(readOnly bool) {
	s.mu.Lock()
	s.readOnly = readOnly
	s.mu.Unlock()
}

// ApprovalMode reports the session's approval axis: the posture
// ("allow"/"ask"/"deny") dangerous calls meet. Guarded by mu.
func (s *agentSession) ApprovalMode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.approvalMode == "" {
		return "ask"
	}
	return s.approvalMode
}

// SetApprovalMode switches the session's approval axis mid-session (the
// approval config option). Effective at the NEXT loop build. An invalid
// value is rejected, not clamped: callers validate against the advertised
// options first.
func (s *agentSession) SetApprovalMode(mode string) error {
	switch mode {
	case "allow", "ask", "deny":
	default:
		return fmt.Errorf("invalid approval mode %q (must be allow, ask, or deny)", mode)
	}
	s.mu.Lock()
	s.approvalMode = mode
	s.mu.Unlock()
	return nil
}

// Mode reports the ACP mode vocabulary value the session's mode axis maps
// to: a read-only session is "plan", everything else "build". Wire
// vocabulary only — the session stores and switches readOnly; this is the
// derivation the session/set_mode and config-option surfaces speak, and it
// no longer depends on which persona the session runs as.
func (s *agentSession) Mode() string {
	if s.IsReadOnly() {
		return acpModePlan
	}
	return acpModeBuild
}

// modeDirective returns the system prompt for the session's persona and
// mode: mainPrompt with one directive injected after the identity block.
// Every session carries a directive so the model can tell which regime it
// is in, who switches it, and that a switch is announced in-band.
func (s *agentSession) modeDirective(persona subagent.Persona) string {
	// Sample the prompt inputs under the session lock: SetPersona swaps
	// mainPrompt/identityEnd/persona and SetReadOnly flips the mode
	// atomically, and the directive must never pair a stale prompt with a
	// new axis value (or vice versa).
	s.mu.RLock()
	main, end, readOnly := s.mainPrompt, s.identityEnd, s.readOnly
	persona, switched := s.persona, s.switched
	s.mu.RUnlock()
	// Level from the same derivation the loop's gate uses, so the directive
	// cannot describe a regime this session does not enforce.
	level := sessionToolsLevel(readOnly)
	lines := []string{
		personaLabel(persona.Name) + " persona is active: " + regimeSentence(level),
		"Only the user switches agents: /agent <name> in the TUI and REPL, the web palette, or ACP session/set_agent. You may ask for a switch with the question tool or in your reply.",
		"An agent-change notice appears in-band in the conversation when the persona switches.",
	}
	if switched {
		// Only after a real switch: earlier persona turns exist in the
		// conversation, and the model must drop that character. A fresh
		// session's first build has no earlier persona — a supersession
		// line there would assert false history.
		lines = append(lines,
			"This persona supersedes any persona named earlier in this conversation: earlier turns may show a different agent persona; ignore that persona's character, voice and style — behave as the persona named above from your next word on.")
	}
	return prompts.InjectAfterIdentityAt(main, end, lines)
}

// regimeSentence states what the tool gate actually enforces at level. It is
// generated from the level, never written per persona, so the directive
// cannot describe a regime the loop does not implement. The wording is the
// mode axis's ("read-only" vs "full"), decoupled from any persona name.
func regimeSentence(level agent.ToolsLevel) string {
	if level == agent.ReadOnlyTools {
		return "read-only mode — inspect and report, do not modify; only the inspection set is available."
	}
	return "full access — the full toolset is available, but do not mutate the workspace or delegate mutating work without approval."
}

// personaLabel renders a persona name as the directive's opening word.
func personaLabel(name string) string {
	if name == "" {
		name = subagent.BuildPersonaName
	}
	return strings.ToUpper(name[:1]) + name[1:]
}
