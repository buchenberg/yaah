package yaah

import (
	"database/sql"
	"errors"
	"log/slog"
	"strings"

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
		"configured", name, "reason", personaMissReason(name))
	p, _ := subagent.ResolvePersona(subagent.BuildPersonaName)
	return sessionPersona{Persona: p, Configured: name, Fallback: true}
}

// resolveSessionPersonaFor picks the persona name a session resolves
// against and runs the D4 chain on it (yaah-21uk.9). A fresh session
// resolves agents.default.agent — the D4 default selection. A resumed
// session resolves its OWN stored sessions.persona name instead: the
// persona is selection state captured at creation, so a later change to
// the config default — or a role file that has since appeared or
// vanished — never rewrites an existing session. What the restored name
// drives is the persona's budget (resolveOrchestratorBudget) and belt
// (buildToolRegistry) effects, the same seams a fresh session feeds;
// the prompt is NOT rebuilt from it — a resumed session's persisted
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

// personaMissReason names why a configured persona did not resolve, so
// the fallback warning can separate a typo from a delegate-only role.
// ResolvePersona collapses both into one miss: mode "subagent" is a
// registered role the orchestrator cannot be; anything else (unknown
// name, no registry) is missing.
func personaMissReason(name string) string {
	if subagent.RoleProfileFor(subagent.SubAgentRole(name)).Mode == "subagent" {
		return "delegate-only"
	}
	return "missing"
}
