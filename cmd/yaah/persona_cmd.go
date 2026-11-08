package yaah

// persona_cmd.go implements the persona-selection UX shared by the
// interactive surfaces (yaah-21uk.10/.12): the REPL /agents and /agent
// commands and the TUI palette handler validate the
// name and SWITCH the current session — the decision reversal recorded
// on yaah-21uk.2 supersedes the fixed-at-construction rule
// (yaah-21uk.11). A switch is effective at the next loop build: the
// persona body rebinds into the prompt, the budget re-resolves, and the
// tool narrowing and read-only gate re-derive (SetPersona). The ACP
// session/set_agent seam still sets the process-level next-session
// override (subagent.SetNextPersona); part 2 reconciles the two. Durable
// selection remains agents.default.agent in the config file, which new
// sessions resolve.

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
// persona marked, and a trailing line naming what the next SESSION would
// resolve — the fresh-session pick after yaah-21uk.10 precedence (the
// switch seam does not touch it).
func (s *agentSession) personaListLines() []string {
	current := s.PersonaName()
	lines := []string{"agent personas — /agent <name> switches this session (next run):"}
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

// switchPersona validates name as a primary-capable persona and switches
// the CURRENT session to it (yaah-21uk.12): SetPersona lands the body,
// budget, tool narrowing and read-only gate at the next loop build. The
// returned string is the reply every surface shows: on ok=false it is
// the refusal (invalid name wording), on ok=true the confirmation naming
// when the switch takes effect. An empty name reports status instead of
// switching. Unlike the Start announcement, a switch TO build is
// announced — the event is the change.
func (s *agentSession) switchPersona(name string) (string, bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return s.personaStatusLine(), true
	}
	if err := s.SetPersona(trimmed); err != nil {
		return err.Error(), false
	}
	return fmt.Sprintf("session persona switched: %s — the new prompt, budgets and tools take effect on the next run", trimmed), true
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
// next persona; a name validates and SWITCHES the current session
// (yaah-21uk.12). Refusals and confirmations both go through the same
// status sink.
func (s *agentSession) agentCommand(arg string) {
	msg := s.personaMsg()
	reply, ok := s.switchPersona(arg)
	if !ok {
		msg("agent: " + reply)
		return
	}
	msg(reply)
}
