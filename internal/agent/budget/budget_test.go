package budget

// Phase 1 of subagent-turn-budget-floors: this suite ports the runner
// characterization tests onto Resolve. It pins the historical precedence
// exactly, including the defects the plan fixes (no floors, unset means
// 3, turns clamped down to iterations-1). Phase 2 edits the relevant
// expectations deliberately.

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
			name:       "BUG (plan §2.2): call=1 starves the role, no floor",
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
			name:       "BUG (plan §2.4): unset means 3",
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
			name:       "BUG (plan §2.2): call=1 beats any role budget, no floor",
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
			name:       "global default is the last named source",
			spec:       Spec{DefaultTurns: 7},
			want:       7,
			wantSource: SourceDefault,
		},
		{
			name:       "explicit zero is unset",
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

func TestResolve_Reconciliation(t *testing.T) {
	tests := []struct {
		name       string
		spec       Spec
		wantIter   int
		wantTurns  int
		wantSource Source
	}{
		{
			name:       "turns never reach iterations",
			spec:       Spec{RoleMaxTurns: 10, RoleMaxIterations: 5},
			wantIter:   5,
			wantTurns:  4,
			wantSource: SourceCeiling,
		},
		{
			name:       "iterations=1 still yields at least 1 turn",
			spec:       Spec{RoleMaxTurns: 10, RoleMaxIterations: 1},
			wantIter:   1,
			wantTurns:  1,
			wantSource: SourceCeiling,
		},
		{
			name:       "unset turns clamped by call iterations",
			spec:       Spec{CallIterations: 2},
			wantIter:   2,
			wantTurns:  1,
			wantSource: SourceCeiling,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := Resolve(tc.spec)
			if b.Iterations != tc.wantIter || b.Turns != tc.wantTurns {
				t.Errorf("budget = %d/%d, want %d/%d",
					b.Iterations, b.Turns, tc.wantIter, tc.wantTurns)
			}
			if b.TurnsSource != tc.wantSource {
				t.Errorf("TurnsSource = %q, want %q", b.TurnsSource, tc.wantSource)
			}
		})
	}
}

func TestResolve_HardCeiling(t *testing.T) {
	t.Run("bounds iterations and turns", func(t *testing.T) {
		b := Resolve(Spec{CallIterations: 100, CallTurns: 80, HardCeiling: 50})
		if b.Iterations != 50 || b.Turns != 49 {
			t.Errorf("budget = %d/%d, want 50/49", b.Iterations, b.Turns)
		}
		if b.IterationsSource != SourceCeiling || b.TurnsSource != SourceCeiling {
			t.Errorf("sources = %q/%q, want ceiling/ceiling", b.IterationsSource, b.TurnsSource)
		}
	})

	t.Run("zero ceiling is a no-op", func(t *testing.T) {
		b := Resolve(Spec{CallIterations: 1000, CallTurns: 1000})
		if b.Iterations != 1000 || b.Turns != 999 {
			t.Errorf("budget = %d/%d, want 1000/999", b.Iterations, b.Turns)
		}
	})
}
