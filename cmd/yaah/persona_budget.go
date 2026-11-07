package yaah

import (
	"github.com/buchenberg/yaah/internal/agent/budget"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
)

// persona_budget.go holds the orchestrator-budget seam (yaah-21uk.8): a
// persona file's max_iterations / max_turns bound the ORCHESTRATOR loop
// the persona drives, not just dispatches of the role. Like the belt
// (persona_belt.go) the resolution is config-authoritative — an
// explicit agents.default value wins, the persona file fills the gap,
// and the shipped defaults apply last — with budget.Resolve supplying
// the precedence rather than a parallel model.

// resolveOrchestratorBudget resolves the loop budget the session's
// orchestrator runs with. The budget.Spec sources mirror how sub-agent
// roles resolve (wiring_stages.go builds the same Spec shape for
// dispatches): the persona file's budgets take Spec's role-file slots,
// the explicit agents.default values (config.ExplicitDefaultBudget,
// captured at Load) take Spec's config slots, and Resolve walks its
// fixed call > config > role file > fallback precedence.
//
// The adapter around Resolve is the mapping the orchestrator needs:
//
//   - Resolve's builtin fallbacks are sub-agent budgets (25
//     iterations; turns = iterations-1). The orchestrator's defaults
//     are instead the loaded agents.default values (50 iterations;
//     max_turns 0 = off), so a fallback-source dimension is remapped
//     onto cfg.Agent.Default. With nothing set anywhere this
//     reproduces today's loop exactly — the zero-budget golden.
//   - No per-call overrides, floors, or schema ceiling exist on the
//     orchestrator path, so the corresponding Spec fields stay zero
//     and Resolve's floor/headroom machinery is inert. The one
//     inherited reconciliation is Resolve's turns >= iterations clamp:
//     a persona declaring at least as many tool turns as loop cycles
//     keeps one iteration of headroom for the forced-text turn, same
//     as dispatch.
//
// The persona file's timeout has no orchestrator knob to land on —
// the loop bounds itself by iterations and turns, not wall-clock — so
// Persona.Timeout is carried but unconsumed here (yaah-21uk.15
// revisits persona-level routing).
func resolveOrchestratorBudget(cfg *config.Config, persona subagent.Persona) budget.Budget {
	b := budget.Resolve(budget.Spec{
		CfgMaxIterations:  cfg.ExplicitDefaultBudget.MaxIterations,
		CfgMaxTurns:       cfg.ExplicitDefaultBudget.MaxTurns,
		RoleMaxIterations: persona.MaxLoopCycles,
		RoleMaxTurns:      persona.MaxToolTurns,
	})
	if b.IterationsSource == budget.SourceFallback {
		b.Iterations = cfg.Agent.Default.MaxLoopCycles
	}
	if b.TurnsSource == budget.SourceFallback {
		b.Turns = cfg.Agent.Default.MaxToolTurns
	}
	return b
}

// orchestratorBudget is the session seam: every loop the session
// builds — interactive (runPrompt), serve (runHeadless), compact —
// takes this budget through loopBuilder.
func (s *agentSession) orchestratorBudget() budget.Budget {
	return resolveOrchestratorBudget(s.cfg, s.persona)
}
