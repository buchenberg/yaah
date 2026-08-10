package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	"github.com/buchenberg/yaah/internal/types"
)

func newTestTraceMiddleware(t *testing.T) *ShepherdTraceMiddleware {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trace.sqlite")
	store, err := shepherd.NewSQLiteTraceStore(path)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return NewShepherdTraceMiddleware(store, "test-session")
}

func TestTraceMiddleware_Name(t *testing.T) {
	m := &ShepherdTraceMiddleware{}
	if m.Name() != "shepherd_trace" {
		t.Errorf("Name() = %q, want %q", m.Name(), "shepherd_trace")
	}
}

func TestTraceMiddleware_PrepareStepNoop(t *testing.T) {
	m := newTestTraceMiddleware(t)
	step := &Step{Messages: []types.Message{{Role: "user", Content: "hello"}}}
	got, err := m.PrepareStep(context.Background(), step)
	if err != nil {
		t.Fatalf("PrepareStep error: %v", err)
	}
	if got != step {
		t.Fatal("PrepareStep should return step unchanged")
	}
}

func TestTraceMiddleware_PostModelNoop(t *testing.T) {
	m := newTestTraceMiddleware(t)
	step := &Step{}
	msg := &types.Message{Role: "assistant", Content: "hi"}
	got, err := m.PostModel(context.Background(), msg, step)
	if err != nil {
		t.Fatalf("PostModel error: %v", err)
	}
	if got != step {
		t.Fatal("PostModel should return step unchanged")
	}
}

func TestTraceMiddleware_PostToolWithNilStore(t *testing.T) {
	m := &ShepherdTraceMiddleware{}
	step := &Step{}
	results := []ToolResult{{Name: "ls", Args: `{"path":"."}`, Error: nil}}
	_, err := m.PostTool(context.Background(), results, step)
	if err != nil {
		t.Fatalf("PostTool with nil store: %v", err)
	}
	if m.ordinal != 0 {
		t.Fatal("ordinal should not advance with nil store")
	}
}

func TestTraceMiddleware_PostToolRecordsDeclaration(t *testing.T) {
	m := newTestTraceMiddleware(t)
	step := &Step{}
	results := []ToolResult{{Name: "ls", Args: `{"path":"."}`, Error: nil}}

	startOrdinal := m.ordinal

	_, err := m.PostTool(context.Background(), results, step)
	if err != nil {
		t.Fatalf("PostTool error: %v", err)
	}

	if m.ordinal != startOrdinal+1 {
		t.Fatalf("ordinal = %d, want %d", m.ordinal, startOrdinal+1)
	}

	slice, err := m.store.ReadOwnerPrefix(shepherd.TrustedReadContext, "test-session", 999, "both")
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}

	factIDs := slice.FactIDs()
	if len(factIDs) != 2 {
		t.Fatalf("fact count = %d, want 2 (declaration + capture)", len(factIDs))
	}

	decl := slice.FactsByID[factIDs[0]]
	cap := slice.FactsByID[factIDs[1]]
	if decl.GetEnvelope().Mode != shepherd.Declaration {
		t.Error("first fact should be a declaration")
	}
	if decl.GetView().KindLabel != "ls" {
		t.Errorf("declaration kind = %q, want %q", decl.GetView().KindLabel, "ls")
	}
	if cap.GetEnvelope().Mode != shepherd.Capture {
		t.Error("second fact should be a capture")
	}
	if cap.GetView().KindLabel != "ls:result" {
		t.Errorf("capture kind = %q, want %q", cap.GetView().KindLabel, "ls:result")
	}

	rec, ok := cap.(shepherd.Record)
	if !ok {
		t.Fatal("capture should be a Record")
	}
	success, _ := rec.Body.Payload["success"].(bool)
	if !success {
		t.Error("capture should have success=true")
	}
}

func TestTraceMiddleware_PostToolRecordsFailure(t *testing.T) {
	m := newTestTraceMiddleware(t)
	results := []ToolResult{{Name: "bash", Args: `{"command":"rm -rf /"}`, Error: os.ErrPermission}}

	_, err := m.PostTool(context.Background(), results, &Step{})
	if err != nil {
		t.Fatalf("PostTool error: %v", err)
	}

	slice, _ := m.store.ReadOwnerPrefix(shepherd.TrustedReadContext, "test-session", 999, "both")
	factIDs := slice.FactIDs()

	rec, _ := slice.FactsByID[factIDs[1]].(shepherd.Record)
	success, _ := rec.Body.Payload["success"].(bool)
	if success {
		t.Error("failed tool should have success=false")
	}
	errMsg, _ := rec.Body.Payload["error"].(string)
	if errMsg == "" {
		t.Error("failed tool should have error message")
	}
}

func TestTraceMiddleware_CausalChaining(t *testing.T) {
	m := newTestTraceMiddleware(t)

	_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "ls", Args: `{}`}}, &Step{})
	_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "read", Args: `{"file":"x"}`}}, &Step{})

	slice, _ := m.store.ReadOwnerPrefix(shepherd.TrustedReadContext, "test-session", 999, "both")
	factIDs := slice.FactIDs()

	if len(factIDs) != 4 {
		t.Fatalf("fact count = %d, want 4 (2 tools × 2 facts each)", len(factIDs))
	}

	// Second tool's declaration should chain from first tool's capture
	secondDecl := slice.FactsByID[factIDs[2]]
	causedBy := secondDecl.GetEnvelope().CausedByIDs
	if len(causedBy) == 0 {
		t.Error("second tool declaration should have causal parents")
	} else if causedBy[0] != factIDs[1] {
		t.Errorf("second tool parent = %s, want %s (first tool capture)", causedBy[0], factIDs[1])
	}
}

func TestTraceMiddleware_MultipleToolsPerCall(t *testing.T) {
	m := newTestTraceMiddleware(t)

	startOrdinal := m.ordinal

	_, _ = m.PostTool(context.Background(), []ToolResult{
		{Name: "read", Args: `{"file":"a"}`},
		{Name: "read", Args: `{"file":"b"}`},
		{Name: "read", Args: `{"file":"c"}`},
	}, &Step{})

	if m.ordinal != startOrdinal+3 {
		t.Fatalf("ordinal = %d, want %d", m.ordinal, startOrdinal+3)
	}

	slice, _ := m.store.ReadOwnerPrefix(shepherd.TrustedReadContext, "test-session", 999, "both")
	if len(slice.FactIDs()) != 6 {
		t.Fatalf("fact count = %d, want 6 (3 tools × 2 facts each)", len(slice.FactIDs()))
	}
}

func TestTraceMiddleware_StartTurn(t *testing.T) {
	m := newTestTraceMiddleware(t)

	startOrdinal := m.ordinal
	m.StartTurn(0, "deepseek-v4", "hello")

	if m.ordinal != startOrdinal+1 {
		t.Fatalf("ordinal = %d, want %d", m.ordinal, startOrdinal+1)
	}
	if len(m.turnRootFactIDs) == 0 {
		t.Fatal("turnRootFactIDs should be set")
	}

	slice, _ := m.store.ReadOwnerPrefix(shepherd.TrustedReadContext, "test-session", 999, "both")
	factIDs := slice.FactIDs()
	if len(factIDs) != 2 {
		t.Fatalf("fact count = %d, want 2 (turn:created + turn:started)", len(factIDs))
	}

	decl := slice.FactsByID[factIDs[0]]
	if decl.GetView().KindLabel != "turn:created" {
		t.Errorf("first fact kind = %q, want turn:created", decl.GetView().KindLabel)
	}
	if decl.GetEnvelope().Mode != shepherd.Declaration {
		t.Error("first fact should be declaration")
	}
}

func TestTraceMiddleware_EndTurnChainsFromTurnRoot(t *testing.T) {
	m := newTestTraceMiddleware(t)
	m.StartTurn(0, "deepseek-v4", "hello")

	// Add a tool call between start and end
	_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "ls", Args: `{}`}}, &Step{})

	m.EndTurn(0, 5000, 200)

	slice, _ := m.store.ReadOwnerPrefix(shepherd.TrustedReadContext, "test-session", 999, "both")
	factIDs := slice.FactIDs()

	// Should be: turn:created, turn:started, ls, ls:result, turn:completed, frontier
	if len(factIDs) != 6 {
		t.Fatalf("fact count = %d, want 6", len(factIDs))
	}

	// turn:completed should chain from turn:created (not the tool result)
	completed := slice.FactsByID[factIDs[4]]
	causedBy := completed.GetEnvelope().CausedByIDs
	if len(causedBy) == 0 {
		t.Fatal("turn:completed should have causal parents")
	}
	if causedBy[0] != factIDs[0] {
		t.Errorf("turn:completed parent = %s, want %s (turn:created)", causedBy[0], factIDs[0])
	}

	rec, _ := completed.(shepherd.Record)
	success, _ := rec.Body.Payload["success"].(bool)
	if !success {
		t.Error("turn:completed should have success=true")
	}
	pt, _ := rec.Body.Payload["prompt_tokens"].(float64)
	ct, _ := rec.Body.Payload["completion_tokens"].(float64)
	if int(pt) != 5000 || int(ct) != 200 {
		t.Errorf("tokens = (%d, %d), want (5000, 200)", int(pt), int(ct))
	}
}

func TestTraceMiddleware_FailTurn(t *testing.T) {
	m := newTestTraceMiddleware(t)
	m.StartTurn(0, "deepseek-v4", "hello")
	m.FailTurn(0, os.ErrNotExist)

	slice, _ := m.store.ReadOwnerPrefix(shepherd.TrustedReadContext, "test-session", 999, "both")
	factIDs := slice.FactIDs()

	// Should be: turn:created, turn:started, turn:failed, frontier
	if len(factIDs) != 4 {
		t.Fatalf("fact count = %d, want 4", len(factIDs))
	}

	failed := slice.FactsByID[factIDs[2]]
	if failed.GetView().KindLabel != "turn:failed" {
		t.Errorf("kind = %q, want turn:failed", failed.GetView().KindLabel)
	}
	rec, _ := failed.(shepherd.Record)
	success, _ := rec.Body.Payload["success"].(bool)
	if success {
		t.Error("turn:failed should have success=false")
	}
	errMsg, _ := rec.Body.Payload["error"].(string)
	if errMsg == "" {
		t.Error("turn:failed should have error message")
	}
}

func TestTraceMiddleware_PublishFrontier(t *testing.T) {
	m := newTestTraceMiddleware(t)
	m.StartTurn(0, "deepseek-v4", "hello")
	m.EndTurn(0, 1000, 100)

	// Frontier should have been published by EndTurn
	store := m.store
	factsCount, _ := store.FactCount()
	if factsCount < 2 {
		t.Fatalf("fact count = %d, want >= 2", factsCount)
	}
}

func TestTraceMiddleware_Close(t *testing.T) {
	m := newTestTraceMiddleware(t)
	if err := m.Close(); err != nil {
		t.Errorf("Close error: %v", err)
	}
	// Closing twice should not panic
	_ = m.Close()
}

func TestTraceMiddleware_Ordinal(t *testing.T) {
	m := newTestTraceMiddleware(t)
	startOrdinal := m.Ordinal()

	m.StartTurn(0, "m", "p")
	if m.Ordinal() != startOrdinal+1 {
		t.Errorf("ordinal after start = %d, want %d", m.Ordinal(), startOrdinal+1)
	}

	// Verify that a second instance gets a different base ordinal
	m2 := newTestTraceMiddleware(t)
	if m2.Ordinal() == startOrdinal {
		t.Errorf("second instance ordinal = %d, should differ from first instance ordinal = %d", m2.Ordinal(), startOrdinal)
	}
}

func TestTraceMiddleware_SessionID(t *testing.T) {
	m := &ShepherdTraceMiddleware{sessionID: "sess-abc"}
	if m.SessionID() != "sess-abc" {
		t.Errorf("SessionID = %q, want %q", m.SessionID(), "sess-abc")
	}
}

func TestTraceMiddleware_StartTurnWithNilStore(t *testing.T) {
	m := &ShepherdTraceMiddleware{}
	m.StartTurn(0, "m", "p")
	if m.ordinal != 0 {
		t.Fatal("ordinal should not advance with nil store")
	}
}

func TestTraceMiddleware_EndTurnWithNilStore(t *testing.T) {
	m := &ShepherdTraceMiddleware{}
	m.EndTurn(0, 100, 50)
	if m.ordinal != 0 {
		t.Fatal("ordinal should not advance with nil store")
	}
}
