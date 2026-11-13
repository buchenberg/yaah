package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/yaah/internal/types"
)

func TestPipeline_OrchestratorTraceBuilder(t *testing.T) {
	if _, ok := builtinBuilders["shepherd_trace"]; !ok {
		t.Error("builtinBuilders is missing shepherd_trace — the orchestrator transcript is part of the trace (yaah-o86)")
	}
	if !slices.Contains(defaultPipelineNames, "shepherd_trace") {
		t.Error("defaultPipelineNames is missing shepherd_trace")
	}
}

func TestPipeline_SubAgentPipelineNames(t *testing.T) {
	names := SubAgentPipelineNames()
	want := []string{"tool_concurrency", "shepherd_trace", "evidence_guard"}
	if len(names) != len(want) {
		t.Fatalf("SubAgentPipelineNames() = %v, want %v", names, want)
	}
	for i, name := range want {
		if names[i] != name {
			t.Errorf("SubAgentPipelineNames()[%d] = %q, want %q", i, names[i], name)
		}
	}
}

func TestPipeline_SubAgentExcludesOrchestrator(t *testing.T) {
	names := SubAgentPipelineNames()
	excluded := []string{"steer", "followup", "approval", "compaction", "loop_detection", "soft_prune"}
	for _, name := range excluded {
		if slices.Contains(names, name) {
			t.Errorf("sub-agent pipeline should not contain orchestrator middleware %q", name)
		}
	}
}

func TestSubAgentTrace_UsesConfiguredStore(t *testing.T) {
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })

	pipe := NewSubAgentPipeline(PipelineConfig{SessionID: "sub-test", TraceStore: store})
	tmw := pipe.ShepherdTraceMiddleware()
	if tmw == nil {
		t.Fatal("expected a real ShepherdTraceMiddleware when the store is configured")
	}
	if tmw.Store() != store {
		t.Error("sub-agent trace middleware should use the session-shared store")
	}
	if tmw.SessionID() != "sub-test" {
		t.Errorf("SessionID = %q, want sub-test", tmw.SessionID())
	}

	// The middleware must NOT close the shared store — closing it would
	// kill tracing for every other consumer in the session.
	if err := tmw.Close(context.Background()); err != nil {
		t.Fatalf("Close on shared-store middleware: %v", err)
	}
	if _, err := store.FactCount(context.Background()); err != nil {
		t.Errorf("shared store unusable after middleware Close: %v", err)
	}
}

func TestSubAgentTrace_NoConfiguredStore(t *testing.T) {
	pipe := NewSubAgentPipeline(PipelineConfig{SessionID: "sub-test"})

	if pipe.ShepherdTraceMiddleware() != nil {
		t.Error("expected no real trace middleware without a configured store")
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
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })

	pipe := NewSubAgentPipeline(PipelineConfig{SessionID: "", TraceStore: store})
	if _, ok := pipe.Find("shepherd_trace").(*noopShepherdTraceMiddleware); !ok {
		t.Error("empty session ID should fall back to the noop trace middleware")
	}
}

func TestInitShepherdInfrastructure(t *testing.T) {
	dir := t.TempDir()

	store, bus, mgr, err := InitShepherdInfrastructure(filepath.Join(dir, "trace.db"), 0)
	if err != nil {
		t.Fatalf("InitShepherdInfrastructure: %v", err)
	}
	defer store.Close(context.Background())

	if store == nil || bus == nil || mgr == nil {
		t.Fatal("store, bus, and manager must all be non-nil")
	}

	// The scope manager creates scopes over the initialized store.
	scope, err := mgr.Create("init-test", nil)
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
		MaxToolConcurrency: 3,
	}
	for _, name := range []string{"tool_concurrency"} {
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

func TestNewFromConfig_ShepherdTraceNoopWithoutStore(t *testing.T) {
	pipe := NewFromConfig(PipelineConfig{SessionID: "test-session"})
	if tmw := pipe.ShepherdTraceMiddleware(); tmw != nil {
		t.Error("without a trace store the orchestrator slot should be a noop")
	}
	if pipe.Find("shepherd_trace") == nil {
		t.Error("orchestrator pipeline should still carry the shepherd_trace slot")
	}
	// The rest of the default pipeline is intact.
	for _, name := range []string{"steer", "followup", "compaction", "approval", "inline_limit", "tool_concurrency", "loop_detection", "evidence_guard", "conflict_detect"} {
		if pipe.Find(name) == nil {
			t.Errorf("default pipeline lost middleware %q", name)
		}
	}
}

// TestNewFromConfig_PromptCachingKnob pins the boolean prompt_caching
// knob (review B10b): setting it includes the middleware without the
// name list, and it stays a single instance.
func TestNewFromConfig_PromptCachingKnob(t *testing.T) {
	pipe := NewFromConfig(PipelineConfig{PromptCaching: true})
	if pipe.Find("prompt_caching") == nil {
		t.Fatal("prompt_caching: true did not include the middleware")
	}
	count := 0
	for _, name := range pipe.MiddlewareNames() {
		if name == "prompt_caching" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("prompt_caching included %d times, want 1", count)
	}
	pipe2 := NewFromConfig(PipelineConfig{})
	if pipe2.Find("prompt_caching") != nil {
		t.Error("prompt_caching middleware present with knob unset")
	}
}

// TestNewFromConfig_ShippedDefaultsParity asserts that an empty config
// resolves to exactly the documented default pipeline — the parity
// gate for the enabled-semantics change.
func TestNewFromConfig_ShippedDefaultsParity(t *testing.T) {
	pipe := NewFromConfig(PipelineConfig{})
	got := pipe.MiddlewareNames()
	if !reflect.DeepEqual(got, defaultPipelineNames) {
		t.Errorf("shipped default pipeline = %v, want %v", got, defaultPipelineNames)
	}
}
