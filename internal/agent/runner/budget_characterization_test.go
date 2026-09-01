package runner

// Characterization tests for sub-agent budget resolution. Phase 0 of
// .agents/plans/subagent-turn-budget-floors pinned the old behaviour
// here; Phase 2 edited this file deliberately to the new semantics —
// the diff is the proof of the fix:
//
//   - a role/config floor (min_turns / min_iterations) now beats any
//     per-call override,
//   - floors grow the other dimension (headroom) instead of shrinking
//     it: turns at or above iterations raise iterations to turns+1,
//   - without a floor, per-call overrides can still force a small
//     budget (deliberate cheap probes stay possible),
//   - the schema hard ceiling (50) bounds the final budget.
//
// Resolution happens in one call — resolveSubAgentBudget — so both
// dimensions reconcile against each other.

import (
	"testing"

	"github.com/buchenberg/yaah/internal/agent/budget"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
)

const charRole = subagent.SubAgentRole("charrole") // never registered: RoleProfileFor returns zero

func resolveBudget(callIter, callTurns int, profile subagent.RoleProfile, subCfg config.SubAgentConfig, role subagent.SubAgentRole) budget.Budget {
	return resolveSubAgentBudget(callIter, callTurns, profile, subCfg, role)
}

func TestResolveSubAgentIterations_Characterization(t *testing.T) {
	tests := []struct {
		name       string
		callMax    int
		profile    subagent.RoleProfile
		subCfg     config.SubAgentConfig
		want       int
		wantSource budget.Source
	}{
		{
			name:       "no inputs falls back to 25",
			want:       25,
			wantSource: budget.SourceFallback,
		},
		{
			name:       "role profile only",
			profile:    subagent.RoleProfile{MaxLoopCycles: 30},
			want:       30,
			wantSource: budget.SourceRoleFile,
		},
		{
			name:       "call above role max is clamped down (ceiling)",
			callMax:    100,
			profile:    subagent.RoleProfile{MaxLoopCycles: 30},
			want:       30,
			wantSource: budget.SourceCeiling,
		},
		{
			name:       "call below role max is kept",
			callMax:    10,
			profile:    subagent.RoleProfile{MaxLoopCycles: 30},
			want:       10,
			wantSource: budget.SourceCall,
		},
		{
			name:       "no floor: call=1 still starves the role",
			callMax:    1,
			profile:    subagent.RoleProfile{MaxLoopCycles: 30},
			want:       1,
			wantSource: budget.SourceCall,
		},
		{
			name:       "FIXED (plan §2.2): role floor beats call=1",
			callMax:    1,
			profile:    subagent.RoleProfile{MaxLoopCycles: 30, MinLoopCycles: 10},
			want:       10,
			wantSource: budget.SourceFloor,
		},
		{
			name: "config role override bypasses the role ceiling",
			subCfg: config.SubAgentConfig{Roles: map[string]config.RoleConfig{
				"charrole": {MaxLoopCycles: 100},
			}},
			profile:    subagent.RoleProfile{MaxLoopCycles: 30},
			want:       50, // plan §4.1: HardCeiling (schema max 50) bounds it
			wantSource: budget.SourceCeiling,
		},
		{
			name:    "call beats config role override",
			callMax: 10,
			subCfg: config.SubAgentConfig{Roles: map[string]config.RoleConfig{
				"charrole": {MaxLoopCycles: 5},
			}},
			want:       10,
			wantSource: budget.SourceCall,
		},
		{
			name: "config role override beats profile",
			subCfg: config.SubAgentConfig{Roles: map[string]config.RoleConfig{
				"charrole": {MaxLoopCycles: 12},
			}},
			profile:    subagent.RoleProfile{MaxLoopCycles: 30},
			want:       12,
			wantSource: budget.SourceRoleConfig,
		},
		{
			name: "FIXED (plan G2): config floor beats role floor",
			subCfg: config.SubAgentConfig{Roles: map[string]config.RoleConfig{
				"charrole": {MinLoopCycles: 15},
			}},
			profile:    subagent.RoleProfile{MaxLoopCycles: 30, MinLoopCycles: 5},
			callMax:    2,
			want:       15,
			wantSource: budget.SourceFloor,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := resolveBudget(tc.callMax, 0, tc.profile, tc.subCfg, charRole)
			if b.Iterations != tc.want {
				t.Errorf("Iterations = %d, want %d", b.Iterations, tc.want)
			}
			if b.IterationsSource != tc.wantSource {
				t.Errorf("IterationsSource = %q, want %q", b.IterationsSource, tc.wantSource)
			}
		})
	}
}

func TestResolveSubAgentTurns_Characterization(t *testing.T) {
	tests := []struct {
		name       string
		callMax    int
		profile    subagent.RoleProfile
		subCfg     config.SubAgentConfig
		want       int
		wantSource budget.Source
	}{
		{
			name:       "FIXED (plan §4.4): unset derives iterations-1, not 3",
			want:       24,
			wantSource: budget.SourceFallback,
		},
		{
			name:       "role profile only",
			profile:    subagent.RoleProfile{MaxToolTurns: 6},
			want:       6,
			wantSource: budget.SourceRoleFile,
		},
		{
			name:       "no floor: call=1 still beats the role budget",
			callMax:    1,
			profile:    subagent.RoleProfile{MaxToolTurns: 40},
			want:       1,
			wantSource: budget.SourceCall,
		},
		{
			name:       "FIXED (plan §2.2): role min_turns beats call=1",
			callMax:    1,
			profile:    subagent.RoleProfile{MaxToolTurns: 40, MinToolTurns: 8},
			want:       8,
			wantSource: budget.SourceFloor,
		},
		{
			name:       "call raises above role budget",
			callMax:    20,
			profile:    subagent.RoleProfile{MaxToolTurns: 6},
			want:       20,
			wantSource: budget.SourceCall,
		},
		{
			name: "config role override beats profile",
			subCfg: config.SubAgentConfig{Roles: map[string]config.RoleConfig{
				"charrole": {MaxToolTurns: 5},
			}},
			profile:    subagent.RoleProfile{MaxToolTurns: 6},
			want:       5,
			wantSource: budget.SourceRoleConfig,
		},
		{
			name:       "global default_max_turns is the last named max source",
			subCfg:     config.SubAgentConfig{DefaultMaxToolTurns: 7},
			want:       7,
			wantSource: budget.SourceDefault,
		},
		{
			name:       "FIXED (plan G2): global default_min_turns floor applies",
			subCfg:     config.SubAgentConfig{DefaultMinToolTurns: 5},
			callMax:    1,
			want:       5,
			wantSource: budget.SourceFloor,
		},
		{
			name: "FIXED (plan G2): config turn floor beats role turn floor",
			subCfg: config.SubAgentConfig{Roles: map[string]config.RoleConfig{
				"charrole": {MinToolTurns: 12},
			}},
			profile:    subagent.RoleProfile{MinToolTurns: 4, MaxToolTurns: 20},
			callMax:    2,
			want:       12,
			wantSource: budget.SourceFloor,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := resolveBudget(0, tc.callMax, tc.profile, tc.subCfg, charRole)
			if b.Turns != tc.want {
				t.Errorf("Turns = %d, want %d", b.Turns, tc.want)
			}
			if b.TurnsSource != tc.wantSource {
				t.Errorf("TurnsSource = %q, want %q", b.TurnsSource, tc.wantSource)
			}
		})
	}
}

// TestResolveSubAgentBudgets_Reconciliation pins headroom reconciliation
// (plan §4.3): a FLOORED turn budget grows iterations instead of
// shrinking. Unfloored turns keep the historical clamp so deliberate
// cheap probes (plan §7 regression) remain forceable.
func TestResolveSubAgentBudgets_Reconciliation(t *testing.T) {
	tests := []struct {
		name      string
		callIter  int
		callTurns int
		profile   subagent.RoleProfile
		wantIter  int
		wantTurns int
	}{
		{
			name:      "no floor: role turns above iterations still clamp down",
			profile:   subagent.RoleProfile{MaxToolTurns: 10, MaxLoopCycles: 5},
			wantIter:  5,
			wantTurns: 4,
		},
		{
			name:      "FIXED (plan §4.3): floor 8 with call iterations 3 -> turns 8, iterations 9",
			callIter:  3,
			profile:   subagent.RoleProfile{MinToolTurns: 8},
			wantIter:  9,
			wantTurns: 8,
		},
		{
			name:      "no floor: small call iterations still clamp large call turns",
			callIter:  2,
			callTurns: 10,
			wantIter:  2,
			wantTurns: 1,
		},
		{
			name:      "hard ceiling bounds headroom growth",
			callIter:  50,
			callTurns: 49,
			profile:   subagent.RoleProfile{},
			wantIter:  50,
			wantTurns: 49,
		},
		{
			name:      "floor unsatisfiable within ceiling clamps turns",
			profile:   subagent.RoleProfile{MinToolTurns: 60},
			wantIter:  50,
			wantTurns: 49,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := resolveBudget(tc.callIter, tc.callTurns, tc.profile, config.SubAgentConfig{}, charRole)
			if b.Iterations != tc.wantIter || b.Turns != tc.wantTurns {
				t.Errorf("budget = %d iterations / %d turns, want %d / %d",
					b.Iterations, b.Turns, tc.wantIter, tc.wantTurns)
			}
		})
	}
}

// TestResolveSubAgentBudgets_ShippedRoles pins the effective budgets of
// the built-in roles. The shipped roles carry no floors yet (Phase 5
// adds them), so their numbers are unchanged by Phase 2.
func TestResolveSubAgentBudgets_ShippedRoles(t *testing.T) {
	initTestRoles(t)

	tests := []struct {
		role      subagent.SubAgentRole
		wantIter  int
		wantTurns int
	}{
		{"analyst", 30, 10},
		{"developer", 40, 6},
		{"tester", 30, 6},
		{"reviewer", 25, 3}, // plan §2.3: retuned in Phase 5
	}
	for _, tc := range tests {
		t.Run(string(tc.role), func(t *testing.T) {
			b := resolveBudget(0, 0, subagent.RoleProfileFor(tc.role), config.SubAgentConfig{}, tc.role)
			if b.Iterations != tc.wantIter || b.Turns != tc.wantTurns {
				t.Errorf("effective budget = %d iterations / %d turns, want %d / %d",
					b.Iterations, b.Turns, tc.wantIter, tc.wantTurns)
			}
		})
	}

	t.Run("no floor: orchestrator can still force a cheap probe", func(t *testing.T) {
		role := subagent.SubAgentRole("reviewer")
		b := resolveBudget(0, 1, subagent.RoleProfileFor(role), config.SubAgentConfig{}, role)
		if b.Turns != 1 {
			t.Errorf("per-call max_turns=1 should win without a floor, got %d", b.Turns)
		}
	})
}
