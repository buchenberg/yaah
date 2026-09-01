package budget

// Table-driven suite for Resolve (ported and extended across the
// subagent-turn-budget-floors phases). Covers the precedence matrix of
// plan §7: sources, floors, headroom reconciliation, and the hard
// ceiling.

import "testing"

func TestResolve_IterationsPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		spec       Spec
		want       int
		wantSource Source
	}{
		{
			name:       "no inputs falls back to 25",
			spec:       Spec{},
			want:       25,
			wantSource: SourceFallback,
		},
		{
			name:       "role file only",
			spec:       Spec{RoleMaxIterations: 30},
			want:       30,
			wantSource: SourceRoleFile,
		},
		{
			name:       "call above role max is clamped down (ceiling)",
			spec:       Spec{CallIterations: 100, RoleMaxIterations: 30},
			want:       30,
			wantSource: SourceCeiling,
		},
		{
			name:       "call below role max is kept",
			spec:       Spec{CallIterations: 10, RoleMaxIterations: 30},
			want:       10,
			wantSource: SourceCall,
		},
		{
			name:       "no floor: call=1 starves the role",
			spec:       Spec{CallIterations: 1, RoleMaxIterations: 30},
			want:       1,
			wantSource: SourceCall,
		},
		{
			name:       "role config bypasses the role ceiling",
			spec:       Spec{CfgMaxIterations: 100, RoleMaxIterations: 30},
			want:       100,
			wantSource: SourceRoleConfig,
		},
		{
			name:       "call beats role config",
			spec:       Spec{CallIterations: 10, CfgMaxIterations: 5},
			want:       10,
			wantSource: SourceCall,
		},
		{
			name:       "role config beats role file",
			spec:       Spec{CfgMaxIterations: 12, RoleMaxIterations: 30},
			want:       12,
			wantSource: SourceRoleConfig,
		},
		{
			name:       "negative inputs are treated as unset",
			spec:       Spec{CallIterations: -5, RoleMaxIterations: -1},
			want:       25,
			wantSource: SourceFallback,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := Resolve(tc.spec)
			if b.Iterations != tc.want {
				t.Errorf("Iterations = %d, want %d", b.Iterations, tc.want)
			}
			if b.IterationsSource != tc.wantSource {
				t.Errorf("IterationsSource = %q, want %q", b.IterationsSource, tc.wantSource)
			}
		})
	}
}

func TestResolve_TurnsPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		spec       Spec
		want       int
		wantSource Source
	}{
		{
			name:       "unset means 3 (plan §4.4 fixes this in Phase 3)",
			spec:       Spec{},
			want:       3,
			wantSource: SourceFallback,
		},
		{
			name:       "role file only",
			spec:       Spec{RoleMaxTurns: 6},
			want:       6,
			wantSource: SourceRoleFile,
		},
		{
			name:       "no floor: call=1 beats any role budget",
			spec:       Spec{CallTurns: 1, RoleMaxTurns: 40},
			want:       1,
			wantSource: SourceCall,
		},
		{
			name:       "call raises above role budget",
			spec:       Spec{CallTurns: 20, RoleMaxTurns: 6},
			want:       20,
			wantSource: SourceCall,
		},
		{
			name:       "role config beats role file",
			spec:       Spec{CfgMaxTurns: 5, RoleMaxTurns: 6},
			want:       5,
			wantSource: SourceRoleConfig,
		},
		{
			name:       "global default is the last named max source",
			spec:       Spec{DefaultTurns: 7},
			want:       7,
			wantSource: SourceDefault,
		},
		{
			name:       "explicit zero is unset (goat-joke-teller case)",
			spec:       Spec{RoleMaxTurns: 0, CallTurns: 0},
			want:       3,
			wantSource: SourceFallback,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := Resolve(tc.spec)
			if b.Turns != tc.want {
				t.Errorf("Turns = %d, want %d", b.Turns, tc.want)
			}
			if b.TurnsSource != tc.wantSource {
				t.Errorf("TurnsSource = %q, want %q", b.TurnsSource, tc.wantSource)
			}
		})
	}
}

func TestResolve_Floors(t *testing.T) {
	tests := []struct {
		name        string
		spec        Spec
		wantIter    int
		wantTurns   int
		wantIterSrc Source
		wantTurnSrc Source
	}{
		{
			name:        "plan §7: call max_turns=1, role min_turns=8 -> floor wins",
			spec:        Spec{CallTurns: 1, RoleMinTurns: 8},
			wantIter:    25, // fallback iterations already cover the floor
			wantTurns:   8,
			wantIterSrc: SourceFallback,
			wantTurnSrc: SourceFloor,
		},
		{
			name:        "plan §7: call max_iterations=3, role min_turns=8 -> headroom grows iterations",
			spec:        Spec{CallIterations: 3, RoleMinTurns: 8},
			wantIter:    9,
			wantTurns:   8,
			wantIterSrc: SourceHeadroom,
			wantTurnSrc: SourceFloor,
		},
		{
			name:        "plan §7: config role floor beats role-file floor",
			spec:        Spec{CallTurns: 2, CfgMinTurns: 10, RoleMinTurns: 4},
			wantIter:    25,
			wantTurns:   10,
			wantIterSrc: SourceFallback,
			wantTurnSrc: SourceFloor,
		},
		{
			name:        "plan §7: DefaultMinTurns applies with no role floor",
			spec:        Spec{CallTurns: 2, DefaultMinTurns: 6},
			wantIter:    25,
			wantTurns:   6,
			wantIterSrc: SourceFallback,
			wantTurnSrc: SourceFloor,
		},
		{
			name:        "role floor precedence: config > role file",
			spec:        Spec{CfgMinTurns: 0, RoleMinTurns: 4},
			wantIter:    25,
			wantTurns:   4,
			wantIterSrc: SourceFallback,
			wantTurnSrc: SourceFloor,
		},
		{
			name:        "iteration floor beats call override",
			spec:        Spec{CallIterations: 2, RoleMinIterations: 12},
			wantIter:    12,
			wantTurns:   3,
			wantIterSrc: SourceFloor,
			wantTurnSrc: SourceFallback,
		},
		{
			name:        "config iteration floor beats role iteration floor",
			spec:        Spec{CallIterations: 2, CfgMinIterations: 15, RoleMinIterations: 8},
			wantIter:    15,
			wantTurns:   3,
			wantIterSrc: SourceFloor,
			wantTurnSrc: SourceFallback,
		},
		{
			name:        "floor already satisfied keeps the original source",
			spec:        Spec{CallTurns: 20, RoleMinTurns: 8},
			wantIter:    25,
			wantTurns:   20,
			wantIterSrc: SourceFallback,
			wantTurnSrc: SourceCall,
		},
		{
			name:        "floor may exceed the role file max (config outranks authorship, plan §10.1)",
			spec:        Spec{CfgMinTurns: 15, RoleMaxTurns: 6},
			wantIter:    25,
			wantTurns:   15,
			wantIterSrc: SourceFallback,
			wantTurnSrc: SourceFloor,
		},
		{
			name:        "negative floor is unset",
			spec:        Spec{CallTurns: 2, RoleMinTurns: -8, DefaultMinTurns: -1},
			wantIter:    25,
			wantTurns:   2,
			wantIterSrc: SourceFallback,
			wantTurnSrc: SourceCall,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := Resolve(tc.spec)
			if b.Iterations != tc.wantIter || b.Turns != tc.wantTurns {
				t.Errorf("budget = %d/%d, want %d/%d",
					b.Iterations, b.Turns, tc.wantIter, tc.wantTurns)
			}
			if b.IterationsSource != tc.wantIterSrc {
				t.Errorf("IterationsSource = %q, want %q", b.IterationsSource, tc.wantIterSrc)
			}
			if b.TurnsSource != tc.wantTurnSrc {
				t.Errorf("TurnsSource = %q, want %q", b.TurnsSource, tc.wantTurnSrc)
			}
		})
	}
}

func TestResolve_DimensionReconciliation(t *testing.T) {
	tests := []struct {
		name        string
		spec        Spec
		wantIter    int
		wantTurns   int
		wantIterSrc Source
		wantTurnSrc Source
	}{
		{
			name:        "floored turns equal iterations grow iterations (headroom)",
			spec:        Spec{RoleMinTurns: 5, RoleMaxIterations: 5},
			wantIter:    6,
			wantTurns:   5,
			wantIterSrc: SourceHeadroom,
			wantTurnSrc: SourceFloor,
		},
		{
			name:        "floored turns above iterations grow iterations (headroom)",
			spec:        Spec{RoleMinTurns: 10, RoleMaxIterations: 5},
			wantIter:    11,
			wantTurns:   10,
			wantIterSrc: SourceHeadroom,
			wantTurnSrc: SourceFloor,
		},
		{
			name:        "unfloored turns at iterations clamp down (cheap probes stay forceable)",
			spec:        Spec{RoleMaxTurns: 5, RoleMaxIterations: 5},
			wantIter:    5,
			wantTurns:   4,
			wantIterSrc: SourceRoleFile,
			wantTurnSrc: SourceCeiling,
		},
		{
			name:        "unfloored turns above iterations clamp down",
			spec:        Spec{RoleMaxTurns: 10, RoleMaxIterations: 5},
			wantIter:    5,
			wantTurns:   4,
			wantIterSrc: SourceRoleFile,
			wantTurnSrc: SourceCeiling,
		},
		{
			name:        "plan §7 regression: call max_iterations=1 still forces exhaustion",
			spec:        Spec{CallIterations: 1, RoleMaxIterations: 25, RoleMaxTurns: 3},
			wantIter:    1,
			wantTurns:   1, // clamp 3 -> 0 -> floor of 1
			wantIterSrc: SourceCall,
			wantTurnSrc: SourceCeiling,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := Resolve(tc.spec)
			if b.Iterations != tc.wantIter || b.Turns != tc.wantTurns {
				t.Errorf("budget = %d/%d, want %d/%d",
					b.Iterations, b.Turns, tc.wantIter, tc.wantTurns)
			}
			if b.IterationsSource != tc.wantIterSrc {
				t.Errorf("IterationsSource = %q, want %q", b.IterationsSource, tc.wantIterSrc)
			}
			if b.TurnsSource != tc.wantTurnSrc {
				t.Errorf("TurnsSource = %q, want %q", b.TurnsSource, tc.wantTurnSrc)
			}
		})
	}
}

func TestResolve_HardCeiling(t *testing.T) {
	t.Run("bounds headroom-grown iterations", func(t *testing.T) {
		b := Resolve(Spec{RoleMinTurns: 80, CallIterations: 10, HardCeiling: 50})
		if b.Iterations != 50 || b.Turns != 49 {
			t.Errorf("budget = %d/%d, want 50/49", b.Iterations, b.Turns)
		}
		if b.IterationsSource != SourceCeiling || b.TurnsSource != SourceCeiling {
			t.Errorf("sources = %q/%q, want ceiling/ceiling", b.IterationsSource, b.TurnsSource)
		}
	})

	t.Run("floor higher than ceiling clamps at the ceiling (validation-time error case)", func(t *testing.T) {
		b := Resolve(Spec{RoleMinTurns: 60, HardCeiling: 50})
		if b.Iterations != 50 || b.Turns != 49 {
			t.Errorf("budget = %d/%d, want 50/49", b.Iterations, b.Turns)
		}
	})

	t.Run("zero ceiling is a no-op", func(t *testing.T) {
		b := Resolve(Spec{CallIterations: 1000, CallTurns: 1000})
		if b.Iterations != 1000 || b.Turns != 999 {
			t.Errorf("budget = %d/%d, want 1000/999", b.Iterations, b.Turns)
		}
	})
}
