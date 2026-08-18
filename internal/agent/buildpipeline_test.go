package agent

import (
	"slices"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
)

func TestBuildPipeline_SubAgentUsesSubAgentPipeline(t *testing.T) {
	l := &Loop{
		CtxMgr: &ContextManager{},
		Config: LoopConfig{IsSubAgent: true, SessionID: "sub-test"},
	}

	names := l.buildPipeline().MiddlewareNames()

	want := pipeline.SubAgentPipelineNames(nil)
	if len(names) != len(want) {
		t.Fatalf("sub-agent pipeline names = %v, want %v", names, want)
	}
	for i, name := range want {
		if names[i] != name {
			t.Errorf("sub-agent pipeline[%d] = %q, want %q", i, names[i], name)
		}
	}

	for _, name := range []string{"steer", "followup", "approval", "compaction", "loop_detection", "staleness"} {
		if slices.Contains(names, name) {
			t.Errorf("sub-agent pipeline should not contain orchestrator middleware %q", name)
		}
	}
}

func TestBuildPipeline_OrchestratorUsesDefault(t *testing.T) {
	l := &Loop{CtxMgr: &ContextManager{}}

	names := l.buildPipeline().MiddlewareNames()

	if slices.Contains(names, "shepherd_trace") {
		t.Error("orchestrator pipeline must not contain shepherd_trace")
	}
	for _, name := range []string{"steer", "followup", "compaction", "approval", "tool_concurrency", "loop_detection", "staleness"} {
		if !slices.Contains(names, name) {
			t.Errorf("orchestrator pipeline missing default middleware %q", name)
		}
	}
}
