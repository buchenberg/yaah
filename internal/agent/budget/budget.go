// Package budget resolves the loop budget a sub-agent runs with.
//
// Two dimensions bound a sub-agent loop:
//
//   - iterations (loop cycles) — a hard stop that yields MaxIterationsError
//   - tool turns — a soft cap that strips the tool list and forces a
//     text-only answer
//
// Budgets come from four sources with fixed precedence — per-call
// override from the orchestrator's tool call, per-role config, the role
// file, and global defaults — and each resolved value carries the Source
// that produced it so traces can answer "who set this budget?".
//
// Resolve is pure: no I/O, no globals, no clock. The runner adapts its
// config/profile/call inputs into a Spec and consumes the Budget.
package budget

// Spec is the complete, explicit input to budget resolution. Every
// source of truth is a named field, so precedence is visible at the
// call site and testable without constructing a runner. Zero (or
// negative) means unset in every field.
type Spec struct {
	// Per-call overrides from the orchestrator's tool call.
	CallIterations int
	CallTurns      int

	// Role file frontmatter (internal/prompts/roles, .agents/roles).
	RoleMaxIterations int
	RoleMinIterations int
	RoleMaxTurns      int
	RoleMinTurns      int

	// config.yaml agents.subagent.roles.<role>.* overrides.
	CfgMaxIterations int
	CfgMinIterations int
	CfgMaxTurns      int
	CfgMinTurns      int

	// config.yaml agents.subagent.* global defaults.
	DefaultTurns    int
	DefaultMinTurns int

	// HardCeiling bounds the final iteration count (tool-schema max).
	// 0 disables the ceiling.
	HardCeiling int
}

// Source identifies which precedence branch supplied a value, for
// tracing and error messages.
type Source string

const (
	SourceCall       Source = "call"           // per-call override from the tool call
	SourceRoleConfig Source = "role_config"    // config.yaml agents.subagent.roles.<role>
	SourceRoleFile   Source = "role_file"      // role frontmatter
	SourceDefault    Source = "config_default" // config.yaml agents.subagent global
	SourceFallback   Source = "builtin_fallback"
	SourceFloor      Source = "floor"    // a min_* raised the value
	SourceCeiling    Source = "ceiling"  // a max_* or reconciliation lowered/clamped it
	SourceHeadroom   Source = "headroom" // reconciliation grew iterations to protect turns
)

func (s Source) String() string { return string(s) }

// Budget is the resolved loop budget plus the provenance of each
// dimension.
type Budget struct {
	Iterations       int
	Turns            int
	IterationsSource Source
	TurnsSource      Source
}

// fallbackIterations is the builtin iteration budget when nothing else
// expresses one.
const fallbackIterations = 25

// fallbackTurns derives the builtin tool-turn budget when nothing else
// expresses one: essentially the whole loop budget, leaving one
// iteration of headroom for the forced-text turn (plan §4.4 — the old
// magic constant 3 starved roles that never declared max_turns, e.g.
// security_auditor).
func fallbackTurns(iterations int) int {
	if iterations > 1 {
		return iterations - 1
	}
	return 1
}

// SchemaMaxIterations is the maximum the spawn_subagent /
// supervised_task tool schemas accept per call. Resolve bounds the
// final budget here so headroom reconciliation cannot grow iterations
// without limit.
const SchemaMaxIterations = 50

// Resolve computes the effective budget for a Spec. Precedence mirrors
// the historical runner behaviour:
//
//   - iterations: call (clamped down to the role-file max) > role
//     config > role file > builtin fallback. Config overrides are
//     authoritative and bypass the role ceiling.
//   - turns: call > role config > role file > global default > builtin
//     fallback.
//
// Floors (min_*) raise either dimension below the declared minimum —
// config floors outrank role-file floors, and a floor beats a per-call
// override. Floors never shrink the other dimension: when floored turns
// reach or exceed iterations, reconciliation GROWS iterations to keep
// one loop cycle of headroom for the forced-text turn (source
// "headroom"). Unfloored turns that reach iterations are clamped down
// historically, so per-call max_iterations alone can still force a
// cheap probe to exhaust.
//
// HardCeiling, when positive, bounds both dimensions after all other
// resolution. A floor the ceiling cannot satisfy is a validation-time
// configuration error, not something resolved here.
func Resolve(s Spec) Budget {
	var b Budget

	switch {
	case s.CallIterations > 0:
		b.Iterations, b.IterationsSource = s.CallIterations, SourceCall
		if s.RoleMaxIterations > 0 && b.Iterations > s.RoleMaxIterations {
			b.Iterations, b.IterationsSource = s.RoleMaxIterations, SourceCeiling
		}
	case s.CfgMaxIterations > 0:
		b.Iterations, b.IterationsSource = s.CfgMaxIterations, SourceRoleConfig
	case s.RoleMaxIterations > 0:
		b.Iterations, b.IterationsSource = s.RoleMaxIterations, SourceRoleFile
	default:
		b.Iterations, b.IterationsSource = fallbackIterations, SourceFallback
	}

	// Iteration floor: config outranks the role file.
	if floor := firstPositive(s.CfgMinIterations, s.RoleMinIterations); floor > 0 && b.Iterations < floor {
		b.Iterations, b.IterationsSource = floor, SourceFloor
	}

	switch {
	case s.CallTurns > 0:
		b.Turns, b.TurnsSource = s.CallTurns, SourceCall
	case s.CfgMaxTurns > 0:
		b.Turns, b.TurnsSource = s.CfgMaxTurns, SourceRoleConfig
	case s.RoleMaxTurns > 0:
		b.Turns, b.TurnsSource = s.RoleMaxTurns, SourceRoleFile
	case s.DefaultTurns > 0:
		b.Turns, b.TurnsSource = s.DefaultTurns, SourceDefault
	default:
		b.Turns, b.TurnsSource = fallbackTurns(b.Iterations), SourceFallback
	}

	// Turn floor: config outranks the role file, then the global default.
	turnsFloored := false
	if floor := firstPositive(s.CfgMinTurns, s.RoleMinTurns, s.DefaultMinTurns); floor > 0 && b.Turns < floor {
		b.Turns, b.TurnsSource = floor, SourceFloor
		turnsFloored = true
	}

	// Reconcile dimensions. A FLOORED turn budget wins over iterations:
	// growing iterations (rather than shrinking turns) is what makes a
	// floor a floor — cutting turns below their floor would reproduce
	// the starvation one level down. Unfloored turns keep the
	// historical clamp down instead, so a per-call max_iterations alone
	// can still force exhaustion for deliberate cheap probes (plan §7
	// regression; this refines §4.2's unconditional growth).
	if turnsFloored && b.Turns >= b.Iterations {
		b.Iterations, b.IterationsSource = b.Turns+1, SourceHeadroom
	} else if b.Iterations > 0 && b.Turns >= b.Iterations {
		b.Turns, b.TurnsSource = b.Iterations-1, SourceCeiling
	}

	// Hard ceiling (schema maximum). Bounds both dimensions; see the
	// validation note above regarding unsatisfiable floors.
	if s.HardCeiling > 0 && b.Iterations > s.HardCeiling {
		b.Iterations, b.IterationsSource = s.HardCeiling, SourceCeiling
		if b.Turns > b.Iterations-1 {
			b.Turns, b.TurnsSource = b.Iterations-1, SourceCeiling
		}
	}

	if b.Turns < 1 {
		b.Turns = 1
	}
	if b.Iterations < 1 {
		b.Iterations = 1
	}

	return b
}

// firstPositive returns the first strictly positive value, or 0.
func firstPositive(vs ...int) int {
	for _, v := range vs {
		if v > 0 {
			return v
		}
	}
	return 0
}

// FirstPositive is the exported floor-precedence helper: the first
// strictly positive value in precedence order (config > role file >
// global default), or 0 when none is set. Consumers that display the
// effective floor (list_subagents) use it to mirror Resolve's ordering.
func FirstPositive(vs ...int) int { return firstPositive(vs...) }
