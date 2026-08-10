package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestShepherdTraceBuilder_NoDirDefaultsToHome(t *testing.T) {
	cfg := PipelineConfig{
		ShepherdTraceDir: "",
		SessionID:        "test",
	}

	mw := builtinBuilders["shepherd_trace"](cfg)
	if mw == nil {
		t.Fatal("builder should return a middleware")
	}
	if mw.Name() != "shepherd_trace" {
		t.Errorf("Name() = %q, want shepherd_trace", mw.Name())
	}

	// With empty dir, the builder falls back to ~/.yaah/traces/.
	// If that directory exists and is writable, a real middleware is built.
	// If not, a noop is returned. Both outcomes are valid.
	switch m := mw.(type) {
	case *ShepherdTraceMiddleware:
		m.Close()
	case *noopShepherdTraceMiddleware:
		// acceptable
	default:
		t.Errorf("unexpected type: %T", mw)
	}
}

func TestShepherdTraceBuilder_WithDirBuildsRealMiddleware(t *testing.T) {
	dir := t.TempDir()
	cfg := PipelineConfig{
		ShepherdTraceDir: dir,
		SessionID:        "test-session",
	}

	mw := builtinBuilders["shepherd_trace"](cfg)
	if mw == nil {
		t.Fatal("builder should return a middleware")
	}

	tm, ok := mw.(*ShepherdTraceMiddleware)
	if !ok {
		t.Fatalf("expected *ShepherdTraceMiddleware, got %T", mw)
	}
	defer tm.Close()

	if tm.SessionID() != "test-session" {
		t.Errorf("SessionID = %q, want test-session", tm.SessionID())
	}

	_, err := os.Stat(filepath.Join(dir, "trace.sqlite"))
	if err != nil {
		t.Errorf("trace store not created: %v", err)
	}
}

func TestShepherdTraceBuilder_UnwritableDirBuildsNoop(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "readonly")
	if err := os.Mkdir(dir, 0o000); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	cfg := PipelineConfig{
		ShepherdTraceDir: dir,
		SessionID:        "test",
	}

	mw := builtinBuilders["shepherd_trace"](cfg)
	_, ok := mw.(*noopShepherdTraceMiddleware)
	if !ok {
		t.Error("unwritable dir should fall back to noop")
	}
}

func TestShepherdTraceBuilder_IsInDefaultPipeline(t *testing.T) {
	for _, name := range defaultPipelineNames {
		if name == "shepherd_trace" {
			return
		}
	}
	t.Error("shepherd_trace is not in defaultPipelineNames")
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

func TestNewFromConfig_IncludesShepherdTrace(t *testing.T) {
	dir := t.TempDir()
	cfg := PipelineConfig{
		ShepherdTraceDir: dir,
		SessionID:        "test-session",
	}
	pipe := NewFromConfig(cfg)
	tmw := pipe.ShepherdTraceMiddleware()
	if tmw == nil {
		t.Fatal("ShepherdTraceMiddleware() should return non-nil when shepherd_trace is in default pipeline")
	}
	tmw.Close()
}

func TestNewFromConfig_SkipsShepherdTraceWhenDisabled(t *testing.T) {
	cfg := PipelineConfig{
		ShepherdTraceDir: "",
		SessionID:        "test-session",
		PipelineDisabled: []string{"shepherd_trace"},
	}
	pipe := NewFromConfig(cfg)
	if tmw := pipe.ShepherdTraceMiddleware(); tmw != nil {
		t.Error("ShepherdTraceMiddleware() should be nil when shepherd_trace is disabled")
	}
}
