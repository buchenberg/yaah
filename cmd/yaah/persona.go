package yaah

import (
	"database/sql"
	"errors"
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
// resolves agents.default.agent — the D4 default selection — and falls
// through the process-level next-session override when that key is
// unset (yaah-21uk.10 precedence for a fresh session: explicit config,
// then the override, then build; the plain string key makes "" mean
// unset, so an override may apply). A resumed session resolves its OWN
// stored sessions.persona name instead: the persona is selection state
// captured at creation, so a later change to the config default — or a
// role file that has since appeared or vanished, or an override a
// pick surface set — never rewrites an existing session. What the
// restored name drives is the persona's budget (resolveOrchestratorBudget)
// and belt (buildToolRegistry) effects, the same seams a fresh session
// feeds; the prompt is NOT rebuilt from it — a resumed session's
// persisted SystemPrompt wins byte-for-byte (restoreSession), so the
// persona body the session actually ran with stays in the prompt it
// recorded.
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
		// The override never reaches the resume path: the fallback handed
		// to storedPersonaName stays the config default, so a failed read
		// degrades to the resolution a store-less build would have made,
		// not to whatever a pick surface last set.
		name = storedPersonaName(db, opts.ResumeSessionID, name)
		source = "the session's stored persona"
		return resolveSessionPersona(source, name)
	}
	if name == "" {
		if override := subagent.NextPersona(); override != "" {
			name = override
			source = "the next-session persona override"
		}
	}
	return resolveSessionPersona(source, name)
}

// storedPersonaName reads a resumed session's persisted persona name.
// Nothing stored — a row from before the persona column existed, the
// zero-change migration default — reads as build: pre-persona sessions
// all ran the plain build orchestrator, so build is what there is to
// restore, and the config default stays out of it (reading "" as the
// config default would let a later config change rewrite old sessions).
// A missing row is not warned about: the resume fails moments later
// with restoreSession's precise "cannot resume session" error, and a
// persona warning ahead of it would only be noise. Any other read
// failure warns and hands back the configured default, the resolution
// a store-less build would make.
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

// PersonaName reports the persona the session runs as, fixed at
// construction (yaah-21uk.11: role reloads refresh the registry for new
// sessions only; nothing re-resolves this mid-session). Immutable after
// wiring, like WorldStatus, so it reads without the session lock. The
// ACP server reports it on session/new and session/load so a client can
// show which persona the session actually is.
func (s *agentSession) PersonaName() string { return s.persona.Name }

// NextPersonaName reports the persona a session built after this call
// would resolve in this process — the yaah-21uk.10 precedence for a
// fresh session: an explicit agents.default.agent config wins, then the
// process-level next-session override a pick surface set, then the
// built-in build persona. It never consults this session's own stored
// persona: that is selection state a resume reads, not something a
// fresh build inherits. The name is the raw resolution input — a
// configured name that no longer resolves still lands on build through
// resolveSessionPersona's warn+fallback at actual build time.
func (s *agentSession) NextPersonaName() string {
	if s.cfg != nil {
		if explicit := strings.TrimSpace(s.cfg.Agent.Default.Agent); explicit != "" {
			return explicit
		}
	}
	if override := subagent.NextPersona(); override != "" {
		return override
	}
	return subagent.BuildPersonaName
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
	name := s.persona.Name
	if name == "" || name == subagent.BuildPersonaName {
		return
	}
	s.dispatchEvent(&agent.AgentChangedEvent{Persona: name, ReadOnly: s.persona.ReadOnly})
}
