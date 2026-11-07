package subagent

import (
	"strings"
	"sync/atomic"
)

// nextPersonaOverride is the process-level persona a FRESH session
// resolves when the config default (agents.default.agent) is unset
// (yaah-21uk.10). Precedence for a fresh session: an explicit
// agents.default.agent config wins, then this override, then the
// built-in build persona. The override never touches a resumed session
// — the stored persona always wins there — and it does not survive a
// restart: it is process state for surfaces that pick a persona
// interactively (REPL /agent, TUI/web palette, ACP session/set_agent).
// Durable selection is agents.default.agent in the config file. The
// zero value (nil) means no override.
var nextPersonaOverride atomic.Pointer[string]

// SetNextPersona sets the process-level next-session persona override;
// an empty (or blank) name clears it. The name is stored trimmed but
// NOT validated here: resolution re-runs the warn-and-fall-back chain,
// so a name that stops resolving later degrades to build exactly like
// a stale config default. Pick surfaces should validate with
// ResolvePersona first so the user gets an error at pick time.
func SetNextPersona(name string) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		nextPersonaOverride.Store(nil)
		return
	}
	nextPersonaOverride.Store(&trimmed)
}

// NextPersona returns the process-level next-session persona override,
// "" when none is set.
func NextPersona() string {
	if p := nextPersonaOverride.Load(); p != nil {
		return *p
	}
	return ""
}

// PersonaMissReason names why a persona name did not resolve, so pick
// surfaces can separate a typo from a delegate-only role in their error
// wording. ResolvePersona collapses both into one miss: mode
// "subagent" is a registered role the orchestrator cannot be; anything
// else (unknown name, no registry) is missing.
func PersonaMissReason(name string) string {
	if RoleProfileFor(SubAgentRole(name)).Mode == "subagent" {
		return "delegate-only"
	}
	return "missing"
}
