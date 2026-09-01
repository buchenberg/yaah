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

// fallbackTurns is the builtin tool-turn budget when nothing else
// expresses one. (Replaced by an iterations-derived value in a later
// phase of subagent-turn-budget-floors; kept here to port the current
// behaviour exactly.)
const fallbackTurns = 3

// Resolve computes the effective budget for a Spec. Precedence mirrors
// the historical runner behaviour:
//
//   - iterations: call (clamped down to the role-file max) > role
//     config > role file > builtin fallback. Config overrides are
//     authoritative and bypass the role ceiling.
//   - turns: call > role config > role file > global default > builtin
//     fallback, then clamped below iterations so at least one loop
//     cycle remains for the forced-text turn.
//
// HardCeiling, when positive, bounds iterations and turns after all
// other resolution.
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
		b.Turns, b.TurnsSource = fallbackTurns, SourceFallback
	}

	// Reconcile dimensions: turns never reach the iteration ceiling,
	// guaranteeing at least one iteration for the forced-text turn.
	if b.Iterations > 0 && b.Turns >= b.Iterations {
		b.Turns, b.TurnsSource = b.Iterations-1, SourceCeiling
	}
	if b.Turns < 1 {
		b.Turns = 1
	}

	// Hard ceiling (schema maximum). No-op while dispatch passes 0.
	if s.HardCeiling > 0 && b.Iterations > s.HardCeiling {
		b.Iterations, b.IterationsSource = s.HardCeiling, SourceCeiling
		if b.Turns > b.Iterations-1 {
			b.Turns, b.TurnsSource = b.Iterations-1, SourceCeiling
		}
		if b.Turns < 1 {
			b.Turns = 1
		}
	}

	return b
}
