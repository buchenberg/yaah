package yaah

import (
	"log/slog"
	"strings"

	"github.com/buchenberg/yaah/internal/agent/subagent"
)

// sessionPersona is the small holder a session build carries the
// resolved orchestrator persona in (yaah-21uk D4). It rides the
// session-config path (sessionConfig.persona); phase 2 prompt binding
// (yaah-21uk.6) consumes Persona. Until then this is pure plumbing:
// resolving it changes no behavior.
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

// resolveSessionPersona applies the D4 selection chain to the
// configured persona name: unset falls through to the built-in "build"
// persona; a name that resolves through the persona view wins;
// anything else warns and falls back to "build". Must run after the
// session's role registry is rebuilt (buildWorkspace) — it resolves
// against whatever the process-wide registry holds at that moment.
func resolveSessionPersona(configured string) sessionPersona {
	name := strings.TrimSpace(configured)
	if name == "" {
		p, _ := subagent.ResolvePersona(subagent.BuildPersonaName)
		return sessionPersona{Persona: p}
	}
	if p, ok := subagent.ResolvePersona(name); ok {
		return sessionPersona{Persona: p, Configured: name}
	}
	slog.Warn("agents.default.agent: not a primary-capable persona; falling back to the default build persona",
		"configured", name, "reason", personaMissReason(name))
	p, _ := subagent.ResolvePersona(subagent.BuildPersonaName)
	return sessionPersona{Persona: p, Configured: name, Fallback: true}
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
