package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

func TestPipeline_OrchestratorNoTraceBuilder(t *testing.T) {
	if _, ok := builtinBuilders["shepherd_trace"]; ok {
		t.Error("builtinBuilders still contains shepherd_trace — orchestrator tracing was removed")
	}
	if slices.Contains(defaultPipelineNames, "shepherd_trace") {
		t.Error("defaultPipelineNames still contains shepherd_trace")
	}
}

func TestPipeline_SubAgentPipelineNames(t *testing.T) {
	names := SubAgentPipelineNames(nil)
	want := []string{"tool_concurrency", "shepherd_trace"}
	if len(names) != len(want) {
		t.Fatalf("SubAgentPipelineNames(nil) = %v, want %v", names, want)
	}
	for i, name := range want {
		if names[i] != name {
			t.Errorf("SubAgentPipelineNames(nil)[%d] = %q, want %q", i, names[i], name)
		}
	}
}

func TestPipeline_SubAgentExcludesOrchestrator(t *testing.T) {
	names := SubAgentPipelineNames(nil)
	excluded := []string{"steer", "followup", "approval", "compaction", "loop_detection", "staleness", "soft_prune"}
	for _, name := range excluded {
		if slices.Contains(names, name) {
			t.Errorf("sub-agent pipeline should not contain orchestrator middleware %q", name)
		}
	}
}

func TestPipeline_SubAgentNamesHonourDisabled(t *testing.T) {
	names := SubAgentPipelineNames([]string{"shepherd_trace"})
	if slices.Contains(names, "shepherd_trace") {
		t.Error("disabled shepherd_trace should be excluded")
	}
	if !slices.Contains(names, "tool_concurrency") {
		t.Error("tool_concurrency should remain when only shepherd_trace is disabled")
	}
}

func TestPipeline_SubAgentIncludesPermissionWhenRulesPresent(t *testing.T) {
	cfg := PipelineConfig{
		PermissionRules: []PermissionRule{{Tool: "bash", Path: "/etc/*", Mode: "deny"}},
	}
	p := NewSubAgentPipeline(cfg)
	if p.Find("permission") == nil {
		t.Fatal("sub-agent pipeline has no permission middleware despite PermissionRules being set (A1)")
	}
}

func TestPipeline_SubAgentOmitsPermissionWithoutRules(t *testing.T) {
	p := NewSubAgentPipeline(PipelineConfig{})
	if p.Find("permission") != nil {
		t.Error("sub-agent pipeline built permission middleware with no rules — should be opt-in")
	}
}

func TestPipeline_SubAgentPermissionHonoursDisabled(t *testing.T) {
	cfg := PipelineConfig{
		PermissionRules:  []PermissionRule{{Tool: "bash", Mode: "deny"}},
		PipelineDisabled: []string{"permission"},
	}
	p := NewSubAgentPipeline(cfg)
	if p.Find("permission") != nil {
		t.Error("permission middleware present despite being in PipelineDisabled")
	}
}

func TestSubAgentTrace_UsesSharedStore(t *testing.T) {
	store, err := shepherd.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	prev := tools.SharedTraceStore
	tools.SharedTraceStore = store
	t.Cleanup(func() { tools.SharedTraceStore = prev })

	pipe := NewSubAgentPipeline(PipelineConfig{SessionID: "sub-test"})
	tmw := pipe.ShepherdTraceMiddleware()
	if tmw == nil {
		t.Fatal("expected a real ShepherdTraceMiddleware when the shared store is set")
	}
	if tmw.Store() != store {
		t.Error("sub-agent trace middleware should use the session-shared store")
	}
	if tmw.SessionID() != "sub-test" {
		t.Errorf("SessionID = %q, want sub-test", tmw.SessionID())
	}

	// The middleware must NOT close the shared store — closing it would
	// kill tracing for every other consumer in the session.
	if err := tmw.Close(); err != nil {
		t.Fatalf("Close on shared-store middleware: %v", err)
	}
	if _, err := store.FactCount(); err != nil {
		t.Errorf("shared store unusable after middleware Close: %v", err)
	}
}

func TestSubAgentTrace_NoSharedManager(t *testing.T) {
	prev := tools.SharedTraceStore
	tools.SharedTraceStore = nil
	t.Cleanup(func() { tools.SharedTraceStore = prev })

	pipe := NewSubAgentPipeline(PipelineConfig{SessionID: "sub-test"})

	if pipe.ShepherdTraceMiddleware() != nil {
		t.Error("expected no real trace middleware without a shared store")
	}
	mw := pipe.Find("shepherd_trace")
	if mw == nil {
		t.Fatal("pipeline should still contain the shepherd_trace slot (noop)")
	}
	if _, ok := mw.(*noopShepherdTraceMiddleware); !ok {
		t.Errorf("expected noop trace middleware, got %T", mw)
	}
}

func TestSubAgentTrace_EmptySessionIDIsNoop(t *testing.T) {
	store, err := shepherd.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	prev := tools.SharedTraceStore
	tools.SharedTraceStore = store
	t.Cleanup(func() { tools.SharedTraceStore = prev })

	pipe := NewSubAgentPipeline(PipelineConfig{SessionID: ""})
	if _, ok := pipe.Find("shepherd_trace").(*noopShepherdTraceMiddleware); !ok {
		t.Error("empty session ID should fall back to the noop trace middleware")
	}
}

func TestInitShepherdInfrastructure(t *testing.T) {
	dir := t.TempDir()

	store, bus, mgr, err := InitShepherdInfrastructure(dir, 0)
	if err != nil {
		t.Fatalf("InitShepherdInfrastructure: %v", err)
	}
	defer store.Close()

	if store == nil || bus == nil || mgr == nil {
		t.Fatal("store, bus, and manager must all be non-nil")
	}

	// The scope manager creates scopes over the initialized store.
	scope, err := mgr.Create("init-test")
	if err != nil {
		t.Fatalf("mgr.Create: %v", err)
	}
	if scope.OwnerID() != "init-test" {
		t.Errorf("scope owner = %q, want init-test", scope.OwnerID())
	}
}

func TestInitShepherdInfrastructure_UnwritableDir(t *testing.T) {
	// A path that cannot be created (a regular file blocks the directory).
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}

	_, _, _, err := InitShepherdInfrastructure(filepath.Join(blocker, "traces"), 0)
	if err == nil {
		t.Error("expected an error when the trace directory cannot be created")
	}
}

func TestNoopShepherdTraceMiddleware(t *testing.T) {
	m := &noopShepherdTraceMiddleware{}

	if m.Name() != "shepherd_trace" {
		t.Errorf("Name() = %q, want shepherd_trace", m.Name())
	}

	step := &Step{Messages: []types.Message{{Role: "user", Content: "hi"}}}
	got, err := m.PrepareStep(context.Background(), step)
	if err != nil || got != step {
		t.Fatal("PrepareStep should be a noop")
	}

	got2, err := m.PostModel(context.Background(), &types.Message{}, step)
	if err != nil || got2 != step {
		t.Fatal("PostModel should be a noop")
	}

	got3, err := m.PostTool(context.Background(), []ToolResult{}, step)
	if err != nil || got3 != step {
		t.Fatal("PostTool should be a noop")
	}
}

func TestBuilders_SharedEntriesProduceSameTypes(t *testing.T) {
	cfg := PipelineConfig{
		PermissionRules:    []PermissionRule{{Tool: "bash", Mode: "deny"}},
		MaxToolConcurrency: 3,
	}
	for _, name := range []string{"permission", "tool_concurrency"} {
		builtin, okB := builtinBuilders[name]
		sub, okS := subAgentBuilders[name]
		if !okB || !okS {
			t.Fatalf("builder %q missing from one of the maps", name)
		}
		if reflect.TypeOf(builtin(cfg)) != reflect.TypeOf(sub(cfg)) {
			t.Errorf("builder %q produces different types across maps", name)
		}
	}
}

func TestPipeline_ToolConcurrencySharesInstance(t *testing.T) {
	shared := NewToolConcurrencyMiddleware(2)
	p := NewFromConfig(PipelineConfig{ToolConc: shared, MaxToolConcurrency: 2})
	mw := p.Find("tool_concurrency")
	if mw == nil {
		t.Fatal("pipeline missing tool_concurrency")
	}
	got, ok := mw.(*ToolConcurrencyMiddleware)
	if !ok {
		t.Fatalf("tool_concurrency is %T, want *ToolConcurrencyMiddleware", mw)
	}
	if got != shared {
		t.Error("pipeline built a second ToolConcurrencyMiddleware instead of sharing the Loop's instance")
	}
}

func TestPipeline_ToolConcurrencyNilFallsBackToConfig(t *testing.T) {
	p := NewFromConfig(PipelineConfig{MaxToolConcurrency: 3})
	mw := p.Find("tool_concurrency")
	got, ok := mw.(*ToolConcurrencyMiddleware)
	if !ok {
		t.Fatalf("tool_concurrency = %v, want *ToolConcurrencyMiddleware", mw)
	}
	if got.max != 3 {
		t.Errorf("tool_concurrency max = %d, want 3", got.max)
	}
}

func TestNewFromConfig_NoShepherdTrace(t *testing.T) {
	pipe := NewFromConfig(PipelineConfig{SessionID: "test-session"})
	if tmw := pipe.ShepherdTraceMiddleware(); tmw != nil {
		t.Error("orchestrator pipeline must not contain shepherd_trace")
	}
	// The rest of the default pipeline is intact.
	for _, name := range []string{"steer", "followup", "compaction", "approval", "inline_limit", "tool_concurrency", "loop_detection", "staleness", "conflict_detect"} {
		if pipe.Find(name) == nil {
			t.Errorf("default pipeline lost middleware %q", name)
		}
	}
}
