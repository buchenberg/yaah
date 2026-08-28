package agent

import (
	"slices"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/events"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/tools"
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

	for _, name := range []string{"steer", "followup", "approval", "compaction", "loop_detection", "conflict_detect"} {
		if slices.Contains(names, name) {
			t.Errorf("sub-agent pipeline should not contain orchestrator middleware %q", name)
		}
	}
}

func TestBuildPipeline_SharesToolConcurrencyInstance(t *testing.T) {
	l := &Loop{
		CtxMgr: &ContextManager{},
		Config: LoopConfig{MaxToolConcurrency: 4},
	}
	l.applyDefaults()
	if l.toolConcurrency == nil {
		t.Fatal("applyDefaults did not create toolConcurrency")
	}
	p := l.buildPipeline()
	mw := p.Find("tool_concurrency")
	if mw == nil {
		t.Fatal("pipeline missing tool_concurrency")
	}
	if mw != pipeline.Middleware(l.toolConcurrency) {
		t.Error("pipeline tool_concurrency is not the Loop's semaphore instance")
	}
}

func TestToPipelineConfig_ApprovalHooksWired(t *testing.T) {
	l := &Loop{
		CtxMgr:   &ContextManager{},
		Registry: tools.NewRegistry(),
		Config:   LoopConfig{ApprovalMode: "ask"},
	}
	l.Hooks = events.NewHookEmitter("", "")
	cfg := l.toPipelineConfig()
	if cfg.ApprovalClassify == nil || cfg.ApprovalApprove == nil || cfg.ApprovalEmitDeny == nil {
		t.Fatal("approval callbacks not wired into PipelineConfig")
	}
	if got := cfg.ApprovalClassify("bash", "{}"); got != pipeline.GateGlobal {
		t.Errorf("classify should flag bash (implements DangerClassifier); got %v", got)
	}
	if got := cfg.ApprovalClassify("read", "{}"); got != pipeline.GatePass {
		t.Errorf("classify should pass read; got %v", got)
	}
}

func TestBuildPipeline_OrchestratorUsesDefault(t *testing.T) {
	l := &Loop{CtxMgr: &ContextManager{}}

	names := l.buildPipeline().MiddlewareNames()

	if slices.Contains(names, "shepherd_trace") {
		t.Error("orchestrator pipeline must not contain shepherd_trace")
	}
	for _, name := range []string{"steer", "followup", "compaction", "approval", "inline_limit", "tool_concurrency", "loop_detection", "conflict_detect"} {
		if !slices.Contains(names, name) {
			t.Errorf("orchestrator pipeline missing default middleware %q", name)
		}
	}
}
