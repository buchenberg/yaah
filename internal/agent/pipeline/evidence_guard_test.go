package pipeline

import (
	"context"
	"testing"
)

func readResult(path, args, content string) ToolResult {
	return ToolResult{Name: "read", Args: args, Result: content}
}

func TestEvidenceGuardNudgesOnRepeatedRead(t *testing.T) {
	m := NewEvidenceGuardMiddleware(2, 0)
	step := &Step{}
	ctx := context.Background()
	args := `{"filePath":"/tmp/a.go"}`

	if _, err := m.PostTool(ctx, []ToolResult{readResult("/tmp/a.go", args, "one")}, step); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PostTool(ctx, []ToolResult{readResult("/tmp/a.go", args, "one")}, step); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PostTool(ctx, []ToolResult{readResult("/tmp/a.go", args, "one")}, step); err != nil {
		t.Fatal(err)
	}
	if len(step.Messages) != 1 {
		t.Fatalf("messages = %d, want one steer", len(step.Messages))
	}
	if step.Messages[0].Role != "user" {
		t.Fatalf("steer role = %q", step.Messages[0].Role)
	}
	// No second nudge while still stale.
	if _, err := m.PostTool(ctx, []ToolResult{readResult("/tmp/a.go", args, "one")}, step); err != nil {
		t.Fatal(err)
	}
	if len(step.Messages) != 1 {
		t.Fatalf("duplicate nudge; messages = %d", len(step.Messages))
	}
}

func TestEvidenceGuardReadRangeCoverage(t *testing.T) {
	m := NewEvidenceGuardMiddleware(10, 0)
	step := &Step{}
	ctx := context.Background()

	first := `{"filePath":"/tmp/a.go","offset":1,"limit":10}`
	if _, err := m.PostTool(ctx, []ToolResult{readResult("/tmp/a.go", first, "alpha")}, step); err != nil {
		t.Fatal(err)
	}
	if m.stale != 0 {
		t.Fatalf("first read stale = %d", m.stale)
	}
	// Fully covered range, unchanged content -> stale.
	if _, err := m.PostTool(ctx, []ToolResult{readResult("/tmp/a.go", `{"filePath":"/tmp/a.go","offset":3,"limit":4}`, "alpha")}, step); err != nil {
		t.Fatal(err)
	}
	if m.stale != 1 {
		t.Fatalf("covered read stale = %d, want 1", m.stale)
	}
	// New range -> new evidence, resets stale.
	if _, err := m.PostTool(ctx, []ToolResult{readResult("/tmp/a.go", `{"filePath":"/tmp/a.go","offset":11,"limit":10}`, "beta")}, step); err != nil {
		t.Fatal(err)
	}
	if m.stale != 0 {
		t.Fatalf("new range stale = %d, want 0", m.stale)
	}
}

func TestEvidenceGuardChangedContentIsNew(t *testing.T) {
	m := NewEvidenceGuardMiddleware(2, 0)
	step := &Step{}
	ctx := context.Background()
	args := `{"filePath":"/tmp/a.go"}`

	m.PostTool(ctx, []ToolResult{readResult("/tmp/a.go", args, "one")}, step)
	m.PostTool(ctx, []ToolResult{readResult("/tmp/a.go", args, "two")}, step)
	if m.stale != 0 {
		t.Fatalf("changed content counted stale: %d", m.stale)
	}
}

func TestEvidenceGuardRepeatedGrep(t *testing.T) {
	m := NewEvidenceGuardMiddleware(10, 0)
	step := &Step{}
	ctx := context.Background()
	args := `{"pattern":"needle","path":"."}`

	m.PostTool(ctx, []ToolResult{{Name: "grep", Args: args, Result: "hit"}}, step)
	m.PostTool(ctx, []ToolResult{{Name: "grep", Args: args, Result: "hit"}}, step)
	if m.stale != 1 {
		t.Fatalf("repeated grep stale = %d, want 1", m.stale)
	}
	m.PostTool(ctx, []ToolResult{{Name: "grep", Args: args, Result: "different"}}, step)
	if m.stale != 0 {
		t.Fatalf("new grep result stale = %d, want 0", m.stale)
	}
	m.PostTool(ctx, []ToolResult{{Name: "grep", Args: `{"pattern":"other","path":"."}`, Result: "hit"}}, step)
	if m.stale != 0 {
		t.Fatalf("new grep pattern stale = %d, want 0", m.stale)
	}
}

func TestEvidenceGuardCanonicalizesArgs(t *testing.T) {
	m := NewEvidenceGuardMiddleware(10, 0)
	step := &Step{}
	ctx := context.Background()
	m.PostTool(ctx, []ToolResult{{Name: "grep", Args: `{"pattern":"x","path":"."}`, Result: "hit"}}, step)
	m.PostTool(ctx, []ToolResult{{Name: "grep", Args: `{"path":".","pattern":"x"}`, Result: "hit"}}, step)
	if m.stale != 1 {
		t.Fatalf("key-order variants counted as new: stale=%d", m.stale)
	}
}

func TestEvidenceGuardMutationResets(t *testing.T) {
	m := NewEvidenceGuardMiddleware(10, 0)
	step := &Step{}
	ctx := context.Background()
	file := `{"filePath":"/tmp/a.go"}`
	m.PostTool(ctx, []ToolResult{readResult("/tmp/a.go", file, "one")}, step)
	m.PostTool(ctx, []ToolResult{readResult("/tmp/a.go", file, "one")}, step)
	if m.stale != 1 {
		t.Fatalf("stale = %d, want 1", m.stale)
	}
	if _, err := m.PostTool(ctx, []ToolResult{{Name: "write", Args: file, Result: "ok"}}, step); err != nil {
		t.Fatal(err)
	}
	if m.stale != 0 {
		t.Fatalf("write did not reset stale: %d", m.stale)
	}
	// Read after the write is new evidence (coverage cleared).
	if _, err := m.PostTool(ctx, []ToolResult{readResult("/tmp/a.go", file, "two")}, step); err != nil {
		t.Fatal(err)
	}
	if m.stale != 0 {
		t.Fatalf("post-write read counted stale: %d", m.stale)
	}
}

func TestEvidenceGuardHalts(t *testing.T) {
	m := NewEvidenceGuardMiddleware(1, 2)
	step := &Step{}
	ctx := context.Background()
	file := `{"filePath":"/tmp/a.go"}`
	m.PostTool(ctx, []ToolResult{readResult("/tmp/a.go", file, "one")}, step)
	m.PostTool(ctx, []ToolResult{readResult("/tmp/a.go", file, "one")}, step)
	_, err := m.PostTool(ctx, []ToolResult{readResult("/tmp/a.go", file, "one")}, step)
	if err == nil {
		t.Fatal("expected halt error")
	}
	if _, ok := err.(EvidenceThrashError); !ok {
		t.Fatalf("err = %T, want EvidenceThrashError", err)
	}
}

func TestEvidenceGuardDisabled(t *testing.T) {
	m := NewEvidenceGuardMiddleware(-1, 0)
	step := &Step{}
	ctx := context.Background()
	file := `{"filePath":"/tmp/a.go"}`
	for i := 0; i < 10; i++ {
		if _, err := m.PostTool(ctx, []ToolResult{readResult("/tmp/a.go", file, "one")}, step); err != nil {
			t.Fatal(err)
		}
	}
	if len(step.Messages) != 0 || m.stale != 0 {
		t.Fatalf("disabled guard acted: stale=%d messages=%d", m.stale, len(step.Messages))
	}
}

func TestEvidenceGuardMixedSearchThrash(t *testing.T) {
	m := NewEvidenceGuardMiddleware(3, 0)
	step := &Step{}
	ctx := context.Background()
	a := `{"filePath":"/tmp/a.go"}`
	b := `{"filePath":"/tmp/b.go"}`

	m.PostTool(ctx, []ToolResult{readResult("/tmp/a.go", a, "alpha")}, step)
	m.PostTool(ctx, []ToolResult{{Name: "grep", Args: `{"pattern":"Context"`, Result: "hits"}}, step)
	m.PostTool(ctx, []ToolResult{readResult("/tmp/b.go", b, "bravo")}, step)
	// Heterogeneous repeats that exact loop detection never sees.
	m.PostTool(ctx, []ToolResult{readResult("/tmp/a.go", a, "alpha")}, step)
	m.PostTool(ctx, []ToolResult{{Name: "grep", Args: `{"pattern":"Context"`, Result: "hits"}}, step)
	m.PostTool(ctx, []ToolResult{readResult("/tmp/b.go", b, "bravo")}, step)

	if len(step.Messages) != 1 {
		t.Fatalf("messages = %d, want one mixed-thrash steer", len(step.Messages))
	}
}
