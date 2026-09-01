package runner

// Characterization tests for sub-agent budget resolution (Phase 0 of
// .agents/plans/subagent-turn-budget-floors). They pin TODAY's behaviour
// of resolveSubAgentIterations / resolveSubAgentTurns — including the
// defects this plan fixes — so the Phase 2 edits land as a deliberate,
// reviewable diff:
//
//   - a per-call max_turns: 1 beats any role budget (no floor),
//   - a per-call max_iterations: 1 starves any role,
//   - unset max_turns silently means 3,
//   - turns are clamped down to maxIter-1 (never grow iterations).
//
// Cases marked "BUG (plan §2.x)" are the ones whose expectations flip
// when the floors land.

import (
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
)

const charRole = subagent.SubAgentRole("charrole") // never registered: RoleProfileFor returns zero

func TestResolveSubAgentIterations_Characterization(t *testing.T) {
	tests := []struct {
		name    string
		callMax int
		profile subagent.RoleProfile
		subCfg  config.SubAgentConfig
		want    int
	}{
		{
			name: "no inputs falls back to 25",
			want: 25,
		},
		{
			name:    "role profile only",
			profile: subagent.RoleProfile{MaxLoopCycles: 30},
			want:    30,
		},
		{
			name:    "call above role max is clamped down (ceiling)",
			callMax: 100,
			profile: subagent.RoleProfile{MaxLoopCycles: 30},
			want:    30,
		},
		{
			name:    "call below role max is kept",
			callMax: 10,
			profile: subagent.RoleProfile{MaxLoopCycles: 30},
			want:    10,
		},
		{
			name:    "BUG (plan §2.2): call=1 starves the role, no floor",
			callMax: 1,
			profile: subagent.RoleProfile{MaxLoopCycles: 30},
			want:    1,
		},
		{
			name: "config role override bypasses the role ceiling",
			subCfg: config.SubAgentConfig{Roles: map[string]config.RoleConfig{
				"charrole": {MaxLoopCycles: 100},
			}},
			profile: subagent.RoleProfile{MaxLoopCycles: 30},
			want:    100,
		},
		{
			name:    "call beats config role override",
			callMax: 10,
			subCfg: config.SubAgentConfig{Roles: map[string]config.RoleConfig{
				"charrole": {MaxLoopCycles: 5},
			}},
			want: 10,
		},
		{
			name: "config role override beats profile",
			subCfg: config.SubAgentConfig{Roles: map[string]config.RoleConfig{
				"charrole": {MaxLoopCycles: 12},
			}},
			profile: subagent.RoleProfile{MaxLoopCycles: 30},
			want:    12,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveSubAgentIterations(tc.callMax, tc.profile, tc.subCfg, charRole); got != tc.want {
				t.Errorf("resolveSubAgentIterations = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestResolveSubAgentTurns_Characterization(t *testing.T) {
	tests := []struct {
		name    string
		callMax int
		profile subagent.RoleProfile
		subCfg  config.SubAgentConfig
		maxIter int
		want    int
	}{
		{
			name:    "BUG (plan §2.4): unset means 3",
			maxIter: 25,
			want:    3,
		},
		{
			name:    "role profile only",
			profile: subagent.RoleProfile{MaxToolTurns: 6},
			maxIter: 25,
			want:    6,
		},
		{
			name:    "BUG (plan §2.2): call=1 beats any role budget, no floor",
			callMax: 1,
			profile: subagent.RoleProfile{MaxToolTurns: 40},
			maxIter: 25,
			want:    1,
		},
		{
			name:    "call raises above role budget",
			callMax: 20,
			profile: subagent.RoleProfile{MaxToolTurns: 6},
			maxIter: 25,
			want:    20,
		},
		{
			name: "config role override beats profile",
			subCfg: config.SubAgentConfig{Roles: map[string]config.RoleConfig{
				"charrole": {MaxToolTurns: 5},
			}},
			profile: subagent.RoleProfile{MaxToolTurns: 6},
			maxIter: 25,
			want:    5,
		},
		{
			name:    "global default_max_turns is the last named source",
			subCfg:  config.SubAgentConfig{DefaultMaxToolTurns: 7},
			maxIter: 25,
			want:    7,
		},
		{
			name:    "turns never reach maxIter",
			profile: subagent.RoleProfile{MaxToolTurns: 10},
			maxIter: 5,
			want:    4,
		},
		{
			name:    "maxIter=1 still yields at least 1 turn",
			profile: subagent.RoleProfile{MaxToolTurns: 10},
			maxIter: 1,
			want:    1,
		},
		{
			name: "config override is also clamped by maxIter",
			subCfg: config.SubAgentConfig{Roles: map[string]config.RoleConfig{
				"charrole": {MaxToolTurns: 9},
			}},
			maxIter: 3,
			want:    2,
		},
		{
			name:    "call is also clamped by maxIter",
			callMax: 30,
			maxIter: 5,
			want:    4,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveSubAgentTurns(tc.callMax, tc.profile, tc.subCfg, charRole, tc.maxIter); got != tc.want {
				t.Errorf("resolveSubAgentTurns = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestResolveSubAgentBudgets_ShippedRoles pins the effective budgets of
// the built-in roles exactly as makeTaskRunner resolves them (iterations
// first, then turns against that result). It also documents the
// mismatches called out in plan §2.3.
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
		{"reviewer", 25, 3}, // plan §2.3: 3 tool turns for a reviewer
	}
	for _, tc := range tests {
		t.Run(string(tc.role), func(t *testing.T) {
			profile := subagent.RoleProfileFor(tc.role)
			subCfg := config.SubAgentConfig{}
			iter := resolveSubAgentIterations(0, profile, subCfg, tc.role)
			turns := resolveSubAgentTurns(0, profile, subCfg, tc.role, iter)
			if iter != tc.wantIter || turns != tc.wantTurns {
				t.Errorf("effective budget = %d iterations / %d turns, want %d / %d",
					iter, turns, tc.wantIter, tc.wantTurns)
			}
		})
	}

	t.Run("BUG (plan §2.2): orchestrator starves the reviewer", func(t *testing.T) {
		role := subagent.SubAgentRole("reviewer")
		profile := subagent.RoleProfileFor(role)
		subCfg := config.SubAgentConfig{}
		iter := resolveSubAgentIterations(0, profile, subCfg, role)
		turns := resolveSubAgentTurns(1, profile, subCfg, role, iter)
		if turns != 1 {
			t.Errorf("per-call max_turns=1 should win today, got %d", turns)
		}
	})

	// Pins the dispatch coupling: turns are clamped by the resolved
	// iterations even when both call overrides are in play.
	coupled := []struct {
		name      string
		callIter  int
		callTurns int
		profile   subagent.RoleProfile
		wantIter  int
		wantTurns int
	}{
		{
			name:      "small call iterations clamp large call turns",
			callIter:  2,
			callTurns: 10,
			wantIter:  2,
			wantTurns: 1,
		},
		{
			name:      "profile ceiling bounds call iterations, turns follow",
			callIter:  100,
			callTurns: 50,
			profile:   subagent.RoleProfile{MaxLoopCycles: 30},
			wantIter:  30,
			wantTurns: 29,
		},
	}
	for _, tc := range coupled {
		t.Run("coupled: "+tc.name, func(t *testing.T) {
			iter := resolveSubAgentIterations(tc.callIter, tc.profile, config.SubAgentConfig{}, charRole)
			turns := resolveSubAgentTurns(tc.callTurns, tc.profile, config.SubAgentConfig{}, charRole, iter)
			if iter != tc.wantIter || turns != tc.wantTurns {
				t.Errorf("budget = %d iterations / %d turns, want %d / %d",
					iter, turns, tc.wantIter, tc.wantTurns)
			}
		})
	}
}
