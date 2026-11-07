package yaah

// persona_cmd.go implements the persona-selection UX shared by the
// interactive surfaces (yaah-21uk.10): the REPL /agents and /agent
// commands, the TUI palette handler, the web action, and the ACP
// session/set_agent seam all validate and set the same process-level
// next-session override (subagent.SetNextPersona) and report through
// the same wording. Selecting NEVER switches the current session — the
// persona is fixed at construction (yaah-21uk.11) — so every reply
// states that the selection applies to new sessions. Durable selection
// remains agents.default.agent in the config file, which also outranks
// the override for fresh sessions.

import (
	"fmt"
	"os"
	"strings"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/control"
)

// personaNames lists the persona identifiers in display order (built-ins
// first, then sorted registry personas) — the available set in error
// wording and picker data.
func personaNames() []string {
	names := make([]string, 0, 8)
	for _, p := range subagent.Personas() {
		names = append(names, p.Name)
	}
	return names
}

// personaDescriptions maps persona name to its one-line description,
// the secondary text the TUI/web pickers render.
func personaDescriptions() map[string]string {
	descs := make(map[string]string, 8)
	for _, p := range subagent.Personas() {
		descs[p.Name] = p.Description
	}
	return descs
}

// personaListLines renders the /agents listing: one line per
// primary-capable persona (name + description), the current session's
// persona marked, and a trailing line naming what the next session
// would resolve — the effective pick after yaah-21uk.10 precedence.
func (s *agentSession) personaListLines() []string {
	current := s.PersonaName()
	lines := []string{"agent personas — /agent <name> selects the persona for new sessions:"}
	for _, p := range subagent.Personas() {
		line := fmt.Sprintf("  %s — %s", p.Name, p.Description)
		if p.Name == current {
			line += "  (this session)"
		}
		lines = append(lines, line)
	}
	lines = append(lines, "next session: "+s.NextPersonaName())
	return lines
}

// personaStatusLine renders /agent with no argument: the current
// session's persona plus what a fresh session would pick.
func (s *agentSession) personaStatusLine() string {
	return fmt.Sprintf("session persona: %s — next session: %s", s.PersonaName(), s.NextPersonaName())
}

// selectNextPersona validates name as a primary-capable persona and
// stores it as the process-level next-session override. The returned
// string is the reply every surface shows: on ok=false it is the
// refusal (invalid name wording), on ok=true the confirmation that
// names what the CURRENT session keeps. An empty name reports status
// instead of selecting.
func (s *agentSession) selectNextPersona(name string) (string, bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return s.personaStatusLine(), true
	}
	if _, resolvable := subagent.ResolvePersona(trimmed); !resolvable {
		return fmt.Sprintf("%q is not a primary-capable persona (%s) — available: %s",
			trimmed, subagent.PersonaMissReason(trimmed), strings.Join(personaNames(), ", ")), false
	}
	subagent.SetNextPersona(trimmed)
	reply := fmt.Sprintf("next session persona: %s — applies to new sessions; this session keeps %s",
		trimmed, s.PersonaName())
	// An explicit config default outranks the override (yaah-21uk.10);
	// say so rather than let the user believe the pick took effect.
	if effective := s.NextPersonaName(); effective != trimmed {
		reply += fmt.Sprintf(" (agents.default.agent %q still wins)", effective)
	}
	return reply, true
}

// personaMsg builds the status sink the persona commands report
// through: the session control channel when one is wired, stderr
// otherwise — the same path reloadRoles uses.
func (s *agentSession) personaMsg() func(string) {
	s.mu.RLock()
	ch := s.ctrlCh
	s.mu.RUnlock()
	return func(text string) {
		if ch != nil {
			select {
			case ch <- &control.Status{Text: text}:
			default:
			}
		} else {
			fmt.Fprintf(os.Stderr, "  %s\n", Dim(text))
		}
	}
}

// showAgents handles REPL /agents: the persona listing, one status line
// per entry.
func (s *agentSession) showAgents() {
	msg := s.personaMsg()
	for _, line := range s.personaListLines() {
		msg(line)
	}
}

// agentCommand handles REPL /agent: no argument reports the current and
// next persona; a name validates and sets the override. Refusals and
// confirmations both go through the same status sink.
func (s *agentSession) agentCommand(arg string) {
	msg := s.personaMsg()
	reply, ok := s.selectNextPersona(arg)
	if !ok {
		msg("agent: " + reply)
		return
	}
	msg(reply)
}
