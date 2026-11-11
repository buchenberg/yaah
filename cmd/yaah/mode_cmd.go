package yaah

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/agent"
)

// modeStatusLine renders /mode with no argument: the current session's
// mode axis.
func (s *agentSession) modeStatusLine() string {
	if s.IsReadOnly() {
		return "session mode: read-only"
	}
	return "session mode: full"
}

// switchMode validates arg against the mode axis vocabulary and switches
// the CURRENT session's mode (the interactive follow-up to ACP
// session/set_mode): "read-only" (plan) withholds the mutating tool set,
// "full" (build) restores it. The plan/build words are accepted because
// that vocabulary survives in the ACP spec surface, but the reply speaks
// the mode axis's own words. Effective at the NEXT run, like every other
// axis switch — an in-flight turn keeps the mode it started with. The
// persona is untouched. An empty argument reports status instead of
// switching; an unknown value is refused, not clamped.
//
// The switch is announced in-band with a ModeChangedEvent — the same
// observable-shape rule a persona switch follows — but switching to the
// mode the session already runs is not a switch: it must not announce.
// Like the ACP surface, the mode choice is NOT persisted: a resume
// re-derives the axis from agents.default.mode (the sessions.mode column
// is vestigial), so a mid-session switch lives only in this process.
func (s *agentSession) switchMode(arg string) (string, bool) {
	trimmed := strings.TrimSpace(strings.ToLower(arg))
	if trimmed == "" {
		return s.modeStatusLine(), true
	}
	var readOnly bool
	switch trimmed {
	case "read-only", "plan":
		readOnly = true
	case "full", "build":
		readOnly = false
	default:
		return fmt.Sprintf("%q is not a mode — available: read-only, full", trimmed), false
	}
	if s.IsReadOnly() == readOnly {
		return fmt.Sprintf("session mode is already %s", s.modeWord()), true
	}
	s.SetReadOnly(readOnly)
	// Announce in-band after the switch, with the regime sentence the next
	// run's directive enforces — identical to what a persona-switch notice
	// carries. dispatchEvent buffers it for views that cannot take writes
	// between runs (REPL, ACP).
	s.dispatchEvent(&agent.ModeChangedEvent{
		ReadOnly: readOnly,
		Detail:   regimeSentence(sessionToolsLevel(readOnly)),
	})
	return fmt.Sprintf("session mode switched: %s — the new tool gate takes effect on the next run", s.modeWord()), true
}

// modeWord renders the mode axis's posture in user-facing words (the ACP
// mode vocabulary is the surface, not the reply).
func (s *agentSession) modeWord() string {
	if s.IsReadOnly() {
		return "read-only"
	}
	return "full"
}

// modeCommand handles REPL /mode: no argument reports the current mode
// axis; a word validates and SWITCHES the current session. Refusals and
// confirmations both go through the same status sink the agent commands
// use.
func (s *agentSession) modeCommand(arg string) {
	msg := s.personaMsg()
	reply, ok := s.switchMode(arg)
	if !ok {
		msg("mode: " + reply)
		return
	}
	msg(reply)
}
