package yaah

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/memory"
)

// sessionPersona is the small holder a session build carries the
// resolved orchestrator persona in (yaah-21uk D4). It rides the
// session-config path (sessionConfig.persona); prompt binding
// (yaah-21uk.6) feeds Persona.Body into the orchestrator system prompt
// as its own layer after the identity. The resolved name is persisted
// on the session row at creation (yaah-21uk.9) and re-resolved from
// the stored name on resume.
type sessionPersona struct {
	// Persona is the resolved view: a synthetic built-in ("build",
	// "plan") or a registry persona with mode "primary" or "all".
	Persona subagent.Persona
	// Configured is the trimmed agents.default.agent value; "" when
	// the key is unset.
	Configured string
	// Fallback reports that a configured name did not resolve — the
	// role is missing or delegate-only — and the session downgraded to
	// the built-in "build" persona (OpenCode semantics: warn and fall
	// back, never fail the session).
	Fallback bool
}

// resolveSessionPersona applies the D4 selection chain to the persona
// name a session resolves against: unset falls through to the built-in
// "build" persona; a name that resolves through the persona view wins;
// anything else warns and falls back to "build". source names where
// the name came from — "agents.default.agent" on a fresh session, the
// session's stored persona on resume — so the fallback warning is
// truthful about the origin. Must run after the session's role registry
// is rebuilt (buildWorkspace) — it resolves against whatever the
// process-wide registry holds at that moment.
func resolveSessionPersona(source, configured string) sessionPersona {
	name := strings.TrimSpace(configured)
	if name == "" {
		p, _ := subagent.ResolvePersona(subagent.BuildPersonaName)
		return sessionPersona{Persona: p}
	}
	if p, ok := subagent.ResolvePersona(name); ok {
		return sessionPersona{Persona: p, Configured: name}
	}
	slog.Warn(source+": not a primary-capable persona; falling back to the default build persona",
		"configured", name, "reason", subagent.PersonaMissReason(name))
	p, _ := subagent.ResolvePersona(subagent.BuildPersonaName)
	return sessionPersona{Persona: p, Configured: name, Fallback: true}
}

// resolveSessionPersonaFor picks the persona name a session resolves
// against and runs the D4 chain on it (yaah-21uk.9). A fresh session
// resolves agents.default.agent — the D4 default selection. A resumed
// session resolves its OWN stored sessions.persona name instead: the
// persona is selection state captured at creation, so a later change to
// the config default — or a role file that has since appeared or vanished
// — never rewrites an existing session. What the restored name drives is
// the persona's budget (resolveOrchestratorBudget) and belt
// (buildToolRegistry) effects, the same seams a fresh session feeds; the
// prompt is NOT rebuilt from it — a resumed session's persisted
// SystemPrompt wins byte-for-byte (restoreSession), so the persona body
// the session actually ran with stays in the prompt it recorded.
//
// A store that cannot serve the read (nil, closed, failing) falls back
// to the configured default with a warning — the resolution today's
// binary would have made — rather than failing the session: persona
// restore is a fidelity feature, not a gate. A stored name that no
// longer resolves lands on build through resolveSessionPersona's
// warn+fallback, exactly as a bad config default would.
func resolveSessionPersonaFor(db *memory.DB, opts SessionOptions, cfg *config.Config) sessionPersona {
	name := cfg.Agent.Default.Agent
	source := "agents.default.agent"
	if opts.ResumeSessionID != "" {
		name = storedPersonaName(db, opts.ResumeSessionID, name)
		source = "the session's stored persona"
	}
	return resolveSessionPersona(source, name)
}

// storedPersonaName reads a resumed session's persisted persona name.
// Nothing stored — a row from before the persona column existed — reads
// as build: pre-persona sessions all ran the plain build orchestrator,
// so build is what there is to restore, and the config default stays
// out of it (reading "" as the config default would let a later config
// change rewrite old sessions). A missing row is not warned about: the
// resume fails moments later with restoreSession's precise "cannot
// resume session" error, and a persona warning ahead of it would only
// be noise. Any other read failure warns and hands back the configured
// default, the resolution a store-less build would make.
func storedPersonaName(db *memory.DB, sessionID, fallback string) string {
	if db == nil {
		return fallback
	}
	stored, err := db.SessionPersona(sessionID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("resume: reading the stored persona failed; falling back to the configured default",
				"session", sessionID, "err", err)
		}
		return fallback
	}
	return stored
}

// PersonaName reports the persona the session currently runs as
// (yaah-21uk.12: a mid-session switch — REPL /agent, the TUI palette —
// re-resolves it under the session lock; role reloads still only refresh
// the registry for new sessions). Guarded by mu like every other mutable
// session field. The ACP server reports it on session/new and
// session/load so a client can show which persona the session actually
// is.
func (s *agentSession) PersonaName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.persona.Name
}

// announcePersona publishes the session-start persona notice when the
// session runs as anything other than the built-in build default
// (yaah-21uk.10): views show "session persona: plan" in-band instead of
// the session's identity being invisible. Build is the silent majority
// — announcing it would add a line to every plain session — and a zero
// persona (hand-built sessions) is no persona at all. dispatchEvent
// buffers for views that cannot take writes between runs (REPL, ACP),
// so the notice there rides the first run's drain.
func (s *agentSession) announcePersona() {
	s.mu.RLock()
	name, readOnly := s.persona.Name, s.readOnly
	s.mu.RUnlock()
	if name == "" || name == subagent.BuildPersonaName {
		return
	}
	s.dispatchEvent(&agent.AgentChangedEvent{Persona: name, ReadOnly: readOnly})
}

// SetPersona switches the session's orchestrator persona mid-session
// (yaah-21uk.12, the decision reversal recorded on yaah-21uk.2 — it
// supersedes the fixed-at-construction rule of yaah-21uk.11). The
// switch is effective at the NEXT loop build: the persona body rebinds
// into the system prompt (from the layers retained at construction)
// and the loop budget re-resolves. The mode axis is NOT touched: the
// persona is the agent axis, and read-only-ness is session state set
// independently (decouple plan Phase 1).
//
// Validation comes first (missing or delegate-only names keep the pick
// surfaces' refusal wording), a switch to the persona the session
// already runs is a no-op, prompts are rebuilt and mutated under the
// session lock, persistence is best-effort ON THE SWITCH (a crash, a
// harness timeout or a kill never reaches close), and the announcement
// is published in-band after the lock is released. Unlike the
// session-start announcement — build is the silent majority there — a
// switch TO build is announced: the event itself is the change.
func (s *agentSession) SetPersona(name string) error {
	trimmed := strings.TrimSpace(name)
	persona, ok := subagent.ResolvePersona(trimmed)
	if !ok {
		return fmt.Errorf("%q is not a primary-capable persona (%s) — available: %s",
			trimmed, subagent.PersonaMissReason(trimmed), strings.Join(personaNames(), ", "))
	}
	s.mu.Lock()
	// A switch to the persona the session already runs is not a switch:
	// it must not announce, persist, or rebuild one.
	if s.persona.Name == persona.Name {
		s.mu.Unlock()
		return nil
	}
	system, main, end, promptChanged := s.rebuildPromptsLocked(persona)
	s.persona = persona
	s.systemPrompt = system
	s.mainPrompt = main
	if promptChanged {
		// The replaced identity rebinds the anchor with the persona: a
		// replace-mode persona changes the identity block's bytes, so the
		// construction-time anchor no longer points past it. Non-replace
		// rebuilds return the same anchor as construction.
		s.identityEnd = end
	}
	// The switch happened: gate the directive's supersession clause on
	// it — earlier persona turns exist in the conversation only after a
	// real switch.
	s.switched = true
	// Routing follows the persona (parity W1, yaah-21uk.15): the file's
	// provider/model replace the session's pick; a persona without
	// routing falls back to the config default.
	s.applyPersonaRouting(persona)
	db, sessionID := s.db, s.sessionID
	s.mu.Unlock()
	// Persist the switch so the selection and its prompt survive the
	// process: resume re-resolves the persona column and reads the
	// stored SystemPrompt verbatim. Best-effort, like the session's
	// other bookkeeping (usage flush, title): the switch has
	// already taken effect in this process and the notice below
	// announces it, so a store failure must not be reported as a refused
	// switch — the warning names the real consequence instead (the next
	// resume falls back to the creation-time persona and prompt).
	if db != nil && sessionID != "" {
		if err := db.SetSessionPersona(sessionID, persona.Name); err != nil {
			slog.Warn("session: persisting persona failed", "persona", persona.Name, "err", err)
		}
		if promptChanged {
			if err := db.SetSessionSystemPrompt(sessionID, system); err != nil {
				slog.Warn("session: persisting the rebuilt system prompt failed", "persona", persona.Name, "err", err)
			}
		}
	}
	// Announce the switch in-band, after the lock is released. The notice
	// carries the regime sentence the next run's directive enforces, so the
	// switch is observable from inside the conversation rather than only
	// from the next prompt's directive. The mode axis is untouched by an
	// agent switch: ReadOnly reports the session's current mode, not the
	// persona's. dispatchEvent buffers it for views that cannot take
	// writes between runs (REPL, ACP).
	readOnly := s.IsReadOnly()
	s.dispatchEvent(&agent.AgentChangedEvent{
		Persona:  persona.Name,
		ReadOnly: readOnly,
		Detail:   regimeSentence(sessionToolsLevel(readOnly)),
	})
	return nil
}

// rebuildPromptsLocked re-renders the system and main prompts with the
// given persona bound in (under `system: replace`, the persona body
// rebinds as the identity layer — see buildMarkedPrompt), from the
// layers captured at construction. Returns the rebuilt prompts, the
// identity anchor for the rebuilt bytes, and whether the caller should
// adopt them. Caller holds s.mu.
//
// The rebuild is only adopted when the layers still derive the session's
// CURRENT systemPrompt — the fresh-construction state, where they differ
// by exactly the persona block. On a resume the persisted SystemPrompt
// won at construction (restoreSession), so these re-derived layers
// describe a DIFFERENT prompt than the one the session actually runs;
// rebuilding from them would rewrite environment, project instructions
// and memory alongside the persona. The recorded prompt stands in that
// case, and the switch lands everywhere else (budget, tools, gate).
// promptChanged reports whether the caller should adopt the new bytes
// and anchor; a session that never captured layers (hand-built tests)
// cannot rebuild at all.
func (s *agentSession) rebuildPromptsLocked(p subagent.Persona) (system, main string, end int, promptChanged bool) {
	if s.promptLayers.Identity == "" {
		return s.systemPrompt, s.mainPrompt, s.identityEnd, false
	}
	// Verify the retained layers still derive the session's CURRENT
	// prompt — rebuild with the CURRENT persona must reproduce
	// s.systemPrompt byte-for-byte, under replace semantics too: the
	// current persona's own replace flag binds its body, so a replace
	// session reproduces its replaced identity. On a resume the
	// persisted prompt won at construction (restoreSession), so the
	// layers describe a different prompt than the session runs;
	// rebuilding from them would rewrite environment, project
	// instructions and memory alongside the persona. The recorded
	// prompt stands in that case, and the switch lands everywhere else
	// (budget, tools, gate).
	if cur, _ := buildMarkedPrompt(s.promptLayers, s.opts.ResumeSessionID, s.promptMemory, s.persona); cur != s.systemPrompt {
		return s.systemPrompt, s.mainPrompt, s.identityEnd, false
	}
	// The anchor travels with the rebuild: under replace the persona
	// body IS the identity, so the construction-time identityEnd does
	// not necessarily point past the rebuilt identity. buildMarkedPrompt
	// returns the exact anchor for the rebuilt bytes — the caller adopts
	// it so directive injection stays after the identity whatever the
	// switch did to it (a non-replace rebuild returns the same anchor as
	// construction).
	system, end = buildMarkedPrompt(s.promptLayers, s.opts.ResumeSessionID, s.promptMemory, p)
	// Re-apply the construction-time tool-guidance suffix: the fresh
	// base is bare buildMainPrompt output, but the session's live prompt
	// carries the guidance blocks appendToolGuidance added — without
	// this, a switch would silently drop the delegate/retained/belt/
	// isolated/one-shot guidance from every subsequent loop.
	main = buildMainPrompt(s.cfg, s.opts, system, end) + s.promptGuidance
	return system, main, end, true
}
