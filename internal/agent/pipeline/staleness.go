package pipeline

import (
	"context"
	"fmt"

	"github.com/buchenberg/yaah/internal/types"
)

// StalenessMiddleware detects when the orchestrator's context has shifted
// between sub-agent dispatch and completion. A context shift occurs when a
// steer or followup message is injected (via PrepareStep or PostTool of
// other middleware) while sub-agents are in flight. Results arriving after
// a shift are annotated with a staleness warning so the orchestrator can
// decide whether to trust them.
//
// The middleware tracks a monotonically increasing epoch that advances
// whenever new context is injected. Sub-agent dispatches record the epoch
// at dispatch time; results arriving at a higher epoch are stale.
type StalenessMiddleware struct {
	epoch         int
	dispatchEpoch int
	pending       bool
}

func (m *StalenessMiddleware) Name() string { return "staleness" }

func (m *StalenessMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	return step, nil
}

func (m *StalenessMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	for _, tc := range msg.ToolCalls {
		if tc.Function.Name == "spawn_subagent" {
			m.pending = true
			m.dispatchEpoch = m.epoch
			break
		}
	}
	return step, nil
}

func (m *StalenessMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	contextShifted := false
	for _, r := range results {
		if r.Name == "steer" || r.Name == "followup" || r.Name == "question" {
			contextShifted = true
		}
	}
	if contextShifted {
		m.epoch++
	}

	if !m.pending {
		return step, nil
	}

	stale := m.epoch > m.dispatchEpoch
	m.pending = false

	if !stale {
		return step, nil
	}

	for i := range results {
		if results[i].Name != "spawn_subagent" {
			continue
		}
		results[i].Result += fmt.Sprintf(
			"\n\n[staleness] The orchestrator context changed while this sub-agent was running "+
				"(epoch %d → %d). Its result may be based on outdated assumptions — "+
				"verify before acting on it.",
			m.dispatchEpoch, m.epoch,
		)
	}
	return step, nil
}
