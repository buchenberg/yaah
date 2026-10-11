package pipeline

import (
	"slices"
	"testing"
)

// TestNewSubAgentPipeline_SteerConditional pins the A2 wiring: the steer
// middleware joins a sub-agent pipeline only when the run carries a
// supervision inject channel — and ahead of tool gating and tracing, so
// injected guidance reaches the model before it repeats the mistake.
func pipelineNames(p *Pipeline) []string {
	names := make([]string, 0, len(p.middleware))
	for _, mw := range p.middleware {
		names = append(names, mw.Name())
	}
	return names
}

func TestNewSubAgentPipeline_SteerConditional(t *testing.T) {
	base := SubAgentPipelineNames(nil)

	// No channel: the pipeline is exactly the base list — unchanged
	// behavior for every run the bridge does not cover.
	p := NewSubAgentPipeline(PipelineConfig{})
	if got, want := len(pipelineNames(p)), len(base); got != want {
		t.Errorf("pipeline without a steer channel has %d middleware, want %d", got, want)
	}

	// With a channel: steer joins, first.
	ch := make(chan string, 1)
	p = NewSubAgentPipeline(PipelineConfig{Steer: ch})
	names := pipelineNames(p)
	if !slices.Contains(names, "steer") {
		t.Errorf("pipeline with a steer channel = %v, want steer present", names)
	}
	if names[0] != "steer" {
		t.Errorf("steer must run first, got %v", names)
	}

	// Disabled by config even with a channel wired.
	p = NewSubAgentPipeline(PipelineConfig{Steer: ch, PipelineDisabled: []string{"steer"}})
	if slices.Contains(pipelineNames(p), "steer") {
		t.Error("steer must respect the disabled list")
	}

	// With permission rules too, steer stays first: the prepends are
	// ordered so the documented invariant holds on the combined prefix.
	p = NewSubAgentPipeline(PipelineConfig{
		Steer:           ch,
		PermissionRules: []PermissionRule{{Tool: "bash"}},
	})
	names = pipelineNames(p)
	if names[0] != "steer" || names[1] != "permission" {
		t.Errorf("combined prefix = %v, want [steer permission ...]", names)
	}
}
