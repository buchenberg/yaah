package yaah

import (
	"testing"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/budget"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
)

// budgetedConfig builds a loaded-config shape: agents.default merged
// over the shipped defaults (50 iterations; max_turns 0 = off), plus
// the explicit-key record Load would have captured.
func budgetedConfig(loadedIters, loadedTurns int, explicit config.ExplicitDefaultBudget) *config.Config {
	return &config.Config{
		Agent: config.AgentConfig{
			Default: config.Defaults{
				MaxLoopCycles: loadedIters,
				MaxToolTurns:  loadedTurns,
			},
		},
		ExplicitDefaultBudget: explicit,
	}
}

// persona budgets a Persona literal the way a primary-capable role
// file resolves it.
func personaBudgets(iters, turns int) subagent.Persona {
	return subagent.Persona{Name: "boss", Mode: "primary", MaxLoopCycles: iters, MaxToolTurns: turns}
}

// TestResolveOrchestratorBudget pins the precedence matrix
// (yaah-21uk.8): explicit agents.default wins, the persona file fills
// the gap, the loaded defaults apply last. The golden case — the
// zero-budget built-in build persona with nothing explicit — must
// resolve to exactly today's loop limits.
func TestResolveOrchestratorBudget(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *config.Config
		persona   subagent.Persona
		wantIters int
		wantTurns int
	}{
		{
			// Golden (yaah-21uk.8): the shipped build persona carries no
			// budget, so resolution must be a no-op — today's limits.
			name:      "golden: zero-budget persona, nothing explicit",
			cfg:       budgetedConfig(50, 0, config.ExplicitDefaultBudget{}),
			persona:   personaBudgets(0, 0),
			wantIters: 50,
			wantTurns: 0,
		},
		{
			name:      "config-only iterations",
			cfg:       budgetedConfig(50, 0, config.ExplicitDefaultBudget{MaxIterations: 42}),
			persona:   personaBudgets(0, 0),
			wantIters: 42,
			wantTurns: 0,
		},
		{
			name:      "config-only turns",
			cfg:       budgetedConfig(50, 0, config.ExplicitDefaultBudget{MaxTurns: 7}),
			persona:   personaBudgets(0, 0),
			wantIters: 50,
			wantTurns: 7,
		},
		{
			name:      "config both",
			cfg:       budgetedConfig(50, 0, config.ExplicitDefaultBudget{MaxIterations: 42, MaxTurns: 7}),
			persona:   personaBudgets(0, 0),
			wantIters: 42,
			wantTurns: 7,
		},
		{
			name:      "file-only iterations",
			cfg:       budgetedConfig(50, 0, config.ExplicitDefaultBudget{}),
			persona:   personaBudgets(30, 0),
			wantIters: 30,
			wantTurns: 0,
		},
		{
			name:      "file-only turns",
			cfg:       budgetedConfig(50, 0, config.ExplicitDefaultBudget{}),
			persona:   personaBudgets(0, 20),
			wantIters: 50,
			wantTurns: 20,
		},
		{
			name:      "both set: config wins iterations, persona fills turns",
			cfg:       budgetedConfig(50, 0, config.ExplicitDefaultBudget{MaxIterations: 42}),
			persona:   personaBudgets(30, 20),
			wantIters: 42,
			wantTurns: 20,
		},
		{
			name:      "both set: config wins turns, persona fills iterations",
			cfg:       budgetedConfig(50, 0, config.ExplicitDefaultBudget{MaxTurns: 7}),
			persona:   personaBudgets(30, 20),
			wantIters: 30,
			wantTurns: 7,
		},
		{
			name:      "both set: config wins both",
			cfg:       budgetedConfig(50, 0, config.ExplicitDefaultBudget{MaxIterations: 42, MaxTurns: 7}),
			persona:   personaBudgets(30, 20),
			wantIters: 42,
			wantTurns: 7,
		},
		{
			// A hand-built Config (no Load, no explicit record) behaves
			// as if no key was explicit — the parity roundtrip shape.
			name:      "hand-built config without explicit record is unchanged",
			cfg:       budgetedConfig(42, 7, config.ExplicitDefaultBudget{}),
			persona:   personaBudgets(0, 0),
			wantIters: 42,
			wantTurns: 7,
		},
		{
			// Inherited budget.Resolve reconciliation: turns >= iterations
			// keeps one loop cycle of headroom for the forced-text turn.
			name:      "turns at iterations keeps headroom",
			cfg:       budgetedConfig(50, 0, config.ExplicitDefaultBudget{}),
			persona:   personaBudgets(10, 10),
			wantIters: 10,
			wantTurns: 9,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveOrchestratorBudget(tt.cfg, tt.persona)
			if got.Iterations != tt.wantIters {
				t.Errorf("Iterations = %d, want %d", got.Iterations, tt.wantIters)
			}
			if got.Turns != tt.wantTurns {
				t.Errorf("Turns = %d, want %d", got.Turns, tt.wantTurns)
			}
		})
	}

	t.Run("sources name the winning layer", func(t *testing.T) {
		b := resolveOrchestratorBudget(
			budgetedConfig(50, 0, config.ExplicitDefaultBudget{MaxIterations: 42}),
			personaBudgets(30, 20))
		if b.IterationsSource != budget.SourceRoleConfig {
			t.Errorf("IterationsSource = %s, want %s (explicit agents.default)", b.IterationsSource, budget.SourceRoleConfig)
		}
		if b.TurnsSource != budget.SourceRoleFile {
			t.Errorf("TurnsSource = %s, want %s (persona file)", b.TurnsSource, budget.SourceRoleFile)
		}
	})
}

// TestLoopBuilderAppliesPersonaBudget pins the seam end to end: every
// loop the session builds carries the resolved orchestrator budget,
// and a zero-budget persona leaves today's limits unchanged.
func TestLoopBuilderAppliesPersonaBudget(t *testing.T) {
	buildPersona, _ := subagent.ResolvePersona(subagent.BuildPersonaName)
	tests := []struct {
		name      string
		cfg       *config.Config
		persona   subagent.Persona
		wantIters int
		wantTurns int
	}{
		{
			name:      "golden: build persona changes nothing",
			cfg:       budgetedConfig(50, 0, config.ExplicitDefaultBudget{}),
			persona:   buildPersona,
			wantIters: 50,
			wantTurns: 0,
		},
		{
			name:      "persona budget narrows the loop",
			cfg:       budgetedConfig(50, 0, config.ExplicitDefaultBudget{}),
			persona:   personaBudgets(30, 20),
			wantIters: 30,
			wantTurns: 20,
		},
		{
			name:      "explicit config outranks the persona",
			cfg:       budgetedConfig(50, 0, config.ExplicitDefaultBudget{MaxIterations: 42, MaxTurns: 7}),
			persona:   personaBudgets(30, 20),
			wantIters: 42,
			wantTurns: 7,
		},
		{
			name:      "hand-built config without explicit record is unchanged",
			cfg:       budgetedConfig(42, 7, config.ExplicitDefaultBudget{}),
			persona:   subagent.Persona{},
			wantIters: 42,
			wantTurns: 7,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &agentSession{cfg: tt.cfg, persona: tt.persona}
			loop := s.loopBuilder(nil, "test-model", nil, "", nil, "", "").Build(agent.LoopBuildOptions{})
			if loop.Config.MaxLoopCycles != tt.wantIters {
				t.Errorf("loop.Config.MaxLoopCycles = %d, want %d", loop.Config.MaxLoopCycles, tt.wantIters)
			}
			if loop.Config.MaxToolTurns != tt.wantTurns {
				t.Errorf("loop.Config.MaxToolTurns = %d, want %d", loop.Config.MaxToolTurns, tt.wantTurns)
			}
		})
	}
}
