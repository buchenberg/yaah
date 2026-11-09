package subagent

import "slices"

// Persona is a primary-capable agent persona (yaah-21uk D4): an
// identity the session's orchestrator can run as. Personas come from
// two places — role files whose mode is "primary" or "all", and two
// synthetic built-ins that have no role file ("build", the default
// orchestrator, and "plan", its read-only sibling). A persona is not a
// dispatch target: primary-only roles are filtered from every
// dispatch-facing surface (see delegatable.go).
type Persona struct {
	// Name is the persona identifier: the rolefile's file stem for
	// registry personas, the built-in constant for the synthetic ones.
	Name string
	// Mode mirrors the source role's capability — "primary" or "all"
	// for registry personas. The synthetic built-ins are "primary".
	Mode string
	// Body is the persona's system-prompt body. The synthetic
	// built-ins leave it empty: "build" keeps the embedded identity
	// (phase 2 binds it), and "plan"'s enforcement wording is
	// generated from the tools level (yaah-21uk.12), not stored here.
	Body string
	// Description is the one-line summary shown to whoever picks a
	// persona, worded like the built-in role files' descriptions.
	Description string
	// Tools narrows the orchestrator's tool surface to the role file's
	// tools list (yaah-21uk.7): at wiring the list is intersected with
	// the configured tool belt (agents.default.tools) and the result
	// becomes the session's belt, so a persona can only ever shrink
	// the surface, never grow it. Empty means NO narrowing — the
	// default full surface, or the configured belt if one is set —
	// never zero tools. The synthetic built-ins leave it empty: build
	// is today's full-surface orchestrator, and plan's restriction is
	// the read-only tools level (yaah-21uk.12), not a belt.
	Tools []string
	// MaxLoopCycles / MaxToolTurns / Timeout carry the role file's
	// budget fields (yaah-21uk.8) and bound the ORCHESTRATOR loop the
	// persona drives, resolved config-authoritatively by
	// resolveOrchestratorBudget: an explicit agents.default value
	// wins, the persona file fills the gap, and the shipped defaults
	// apply last (budget.Resolve supplies the precedence). Zero means
	// no constraint, so the synthetic built-ins — all three zero —
	// keep today's limits exactly. Timeout is carried for parity with
	// RoleProfile, but the orchestrator loop has no wall-clock timeout
	// knob yet, so nothing consumes it; the min_* floors are
	// dispatch-specific (they bound per-call overrides) and are not
	// part of the persona view.
	MaxLoopCycles int
	MaxToolTurns  int
	Timeout       int
	// ReadOnly marks personas whose session must not mutate the
	// workspace. Only the synthetic "plan" persona sets it.
	ReadOnly bool
}

// The synthetic built-in personas. They are not files in the registry,
// so a rolefile cannot be missing them; like the registry's own
// built-ins (LoadDir), they take precedence over same-named role files
// — a user role named "build" or "plan" is shadowed, never merged.
const (
	// BuildPersonaName is the default primary persona: today's
	// orchestrator exactly as it ships, full tool surface.
	BuildPersonaName = "build"
	// PlanPersonaName is the read-only planning persona.
	PlanPersonaName = "plan"
)

var builtinPersonas = map[string]Persona{
	BuildPersonaName: {
		Name:        BuildPersonaName,
		Mode:        "primary",
		Description: "Default coding agent. Full tool surface.",
	},
	PlanPersonaName: {
		Name:        PlanPersonaName,
		Mode:        "primary",
		ReadOnly:    true,
		Description: "Explores and plans without editing; read-only enforcement.",
	},
}

// primaryCapableMode reports whether a normalized role mode admits the
// role as an orchestrator persona. The vocabulary is validated at load
// (normalizeRoleMode), so only the two exact values qualify; anything
// else — including the zero mode of an unpopulated profile — is
// delegate-only.
func primaryCapableMode(mode string) bool {
	return mode == "primary" || mode == "all"
}

// Personas enumerates the primary-capable personas available to a
// session: every registered role whose mode is "primary" or "all",
// plus the two synthetic built-ins. The built-ins come first in a
// fixed order (build, plan); registry personas follow sorted by name,
// so the list is deterministic despite the registry's map-backed
// storage.
//
// A rolefile named "build" or "plan" is shadowed by the same-named
// built-in — built-in precedence, mirroring RoleRegistry.LoadDir's
// rule that user files cannot displace registry built-ins.
// Delegate-only roles (mode "subagent", explicit or the missing-key
// default) are not personas and never appear. The built-ins resolve
// even with no registry installed.
func Personas() []Persona {
	out := make([]Persona, 0, len(builtinPersonas))
	out = append(out, builtinPersonas[BuildPersonaName], builtinPersonas[PlanPersonaName])

	reg := DefaultRegistry()
	if reg == nil {
		return out
	}

	reg.mu.RLock()
	names := make([]string, 0, len(reg.entries))
	for roleName, def := range reg.entries {
		if _, builtin := builtinPersonas[string(roleName)]; builtin {
			continue // built-in has precedence
		}
		if !primaryCapableMode(def.Mode) {
			continue
		}
		names = append(names, string(roleName))
	}
	reg.mu.RUnlock()

	slices.Sort(names)
	for _, name := range names {
		if p, ok := registryPersona(reg, SubAgentRole(name)); ok {
			out = append(out, p)
		}
	}
	return out
}

// ResolvePersona resolves a persona by name across the synthetic
// built-ins and the default role registry, built-ins first. The bool
// is false when the name is unknown or names a delegate-only role —
// deciding what to do about a miss (warn and fall back, refuse) is
// the caller's business.
func ResolvePersona(name string) (Persona, bool) {
	if p, ok := builtinPersonas[name]; ok {
		return p, true
	}
	reg := DefaultRegistry()
	if reg == nil {
		return Persona{}, false
	}
	return registryPersona(reg, SubAgentRole(name))
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

// registryPersona converts one registry entry into its persona view,
// reporting false when the entry is missing or not primary-capable.
func registryPersona(reg *RoleRegistry, role SubAgentRole) (Persona, bool) {
	reg.mu.RLock()
	def, ok := reg.entries[role]
	reg.mu.RUnlock()
	if !ok || !primaryCapableMode(def.Mode) {
		return Persona{}, false
	}
	return Persona{
		Name:          string(role),
		Mode:          def.Mode,
		Body:          def.Body,
		Description:   def.Description,
		Tools:         def.Tools,
		MaxLoopCycles: def.MaxLoopCycles,
		MaxToolTurns:  def.MaxToolTurns,
		Timeout:       def.Timeout,
	}, true
}
