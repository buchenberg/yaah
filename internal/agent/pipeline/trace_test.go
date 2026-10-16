package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	shepherdtrace "github.com/buchenberg/shepherd-kernel-go"
	"github.com/buchenberg/yaah/internal/types"
)

func newTestTraceMiddleware(t *testing.T) *ShepherdTraceMiddleware {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trace.sqlite")
	store, err := shepherdtrace.NewSQLiteTraceStore(path)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
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

	slice, err := m.store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, "test-session", 999, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}

	factIDs := slice.FactIDs()
	if len(factIDs) != 2 {
		t.Fatalf("fact count = %d, want 2 (declaration + capture)", len(factIDs))
	}

	decl := slice.FactsByID[factIDs[0]]
	cap := slice.FactsByID[factIDs[1]]
	if decl.GetEnvelope().Mode != shepherdtrace.Declaration {
		t.Error("first fact should be a declaration")
	}
	if decl.GetView().KindLabel != "ls" {
		t.Errorf("declaration kind = %q, want %q", decl.GetView().KindLabel, "ls")
	}
	if cap.GetEnvelope().Mode != shepherdtrace.Capture {
		t.Error("second fact should be a capture")
	}
	if cap.GetView().KindLabel != "ls:result" {
		t.Errorf("capture kind = %q, want %q", cap.GetView().KindLabel, "ls:result")
	}

	rec, ok := cap.(shepherdtrace.Record)
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

	slice, _ := m.store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, "test-session", 999, shepherdtrace.ModeBoth)
	factIDs := slice.FactIDs()

	rec, _ := slice.FactsByID[factIDs[1]].(shepherdtrace.Record)
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

	slice, _ := m.store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, "test-session", 999, shepherdtrace.ModeBoth)
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

	slice, _ := m.store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, "test-session", 999, shepherdtrace.ModeBoth)
	if len(slice.FactIDs()) != 6 {
		t.Fatalf("fact count = %d, want 6 (3 tools × 2 facts each)", len(slice.FactIDs()))
	}
}

func TestTraceMiddleware_StartTurn(t *testing.T) {
	m := newTestTraceMiddleware(t)

	startOrdinal := m.ordinal
	m.StartTurn(context.Background(), 0, "deepseek-v4", "hello")

	if m.ordinal != startOrdinal+1 {
		t.Fatalf("ordinal = %d, want %d", m.ordinal, startOrdinal+1)
	}
	if len(m.turnRootFactIDs) == 0 {
		t.Fatal("turnRootFactIDs should be set")
	}

	slice, _ := m.store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, "test-session", 999, shepherdtrace.ModeBoth)
	factIDs := slice.FactIDs()
	if len(factIDs) != 2 {
		t.Fatalf("fact count = %d, want 2 (turn:created + turn:started)", len(factIDs))
	}

	decl := slice.FactsByID[factIDs[0]]
	if decl.GetView().KindLabel != "turn:created" {
		t.Errorf("first fact kind = %q, want turn:created", decl.GetView().KindLabel)
	}
	if decl.GetEnvelope().Mode != shepherdtrace.Declaration {
		t.Error("first fact should be declaration")
	}
}

func TestTraceMiddleware_EndTurnChainsFromTurnRoot(t *testing.T) {
	m := newTestTraceMiddleware(t)
	m.StartTurn(context.Background(), 0, "deepseek-v4", "hello")

	// Add a tool call between start and end
	_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "ls", Args: `{}`}}, &Step{})

	m.EndTurn(context.Background(), 0, 5000, 200)

	slice, _ := m.store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, "test-session", 999, shepherdtrace.ModeBoth)
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

	rec, _ := completed.(shepherdtrace.Record)
	success, _ := rec.Body.Payload["success"].(bool)
	if !success {
		t.Error("turn:completed should have success=true")
	}
	// Numbers ride as canonical strings since the kernel's canonical-parity
	// work (v0.5.0): the middleware wrote ints, the store rounds them
	// through, so the read tolerates both encodings.
	num := func(v any) string {
		switch n := v.(type) {
		case string:
			return n
		case json.Number:
			return n.String()
		case float64:
			return fmt.Sprintf("%d", int(n))
		default:
			return "?"
		}
	}
	pt := num(rec.Body.Payload["prompt_tokens"])
	ct := num(rec.Body.Payload["completion_tokens"])
	if pt != "5000" || ct != "200" {
		t.Errorf("tokens = (%s, %s), want (5000, 200)", pt, ct)
	}
}

func TestTraceMiddleware_FailTurn(t *testing.T) {
	m := newTestTraceMiddleware(t)
	m.StartTurn(context.Background(), 0, "deepseek-v4", "hello")
	m.FailTurn(context.Background(), 0, os.ErrNotExist)

	slice, _ := m.store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, "test-session", 999, shepherdtrace.ModeBoth)
	factIDs := slice.FactIDs()

	// Should be: turn:created, turn:started, turn:failed, frontier
	if len(factIDs) != 4 {
		t.Fatalf("fact count = %d, want 4", len(factIDs))
	}

	failed := slice.FactsByID[factIDs[2]]
	if failed.GetView().KindLabel != "turn:failed" {
		t.Errorf("kind = %q, want turn:failed", failed.GetView().KindLabel)
	}
	rec, _ := failed.(shepherdtrace.Record)
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
	m.StartTurn(context.Background(), 0, "deepseek-v4", "hello")
	m.EndTurn(context.Background(), 0, 1000, 100)

	// Frontier should have been published by EndTurn
	store := m.store
	factsCount, _ := store.FactCount(context.Background())
	if factsCount < 2 {
		t.Fatalf("fact count = %d, want >= 2", factsCount)
	}
}

func TestTraceMiddleware_Close(t *testing.T) {
	m := newTestTraceMiddleware(t)
	if err := m.Close(context.Background()); err != nil {
		t.Errorf("Close error: %v", err)
	}
	// Closing twice should not panic
	_ = m.Close(context.Background())
}

func TestTraceMiddleware_Ordinal(t *testing.T) {
	m := newTestTraceMiddleware(t)
	startOrdinal := m.Ordinal()

	m.StartTurn(context.Background(), 0, "m", "p")
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
	m.StartTurn(context.Background(), 0, "m", "p")
	if m.ordinal != 0 {
		t.Fatal("ordinal should not advance with nil store")
	}
}

func TestTraceMiddleware_EndTurnWithNilStore(t *testing.T) {
	m := &ShepherdTraceMiddleware{}
	m.EndTurn(context.Background(), 0, 100, 50)
	if m.ordinal != 0 {
		t.Fatal("ordinal should not advance with nil store")
	}
}

// TestTraceMiddleware_BashDeclarationCarriesCmd pins the supervision
// event shape (shepherd-adoption plan B1): the kernel's advisory
// destructive rule reads the shell command from the declaration
// payload's "cmd", while yaah's tool schema calls the field "command" —
// the middleware copies it across so rule events and trace facts stay
// one shape. Non-bash tools carry no "cmd".
func TestTraceMiddleware_BashDeclarationCarriesCmd(t *testing.T) {
	m := newTestTraceMiddleware(t)

	if _, err := m.PostTool(context.Background(), []ToolResult{
		{Name: "bash", Args: `{"command":"rm -rf /tmp/probe"}`},
	}, &Step{}); err != nil {
		t.Fatalf("PostTool error: %v", err)
	}
	if _, err := m.PostTool(context.Background(), []ToolResult{
		{Name: "ls", Args: `{"path":"."}`},
	}, &Step{}); err != nil {
		t.Fatalf("PostTool error: %v", err)
	}

	slice, err := m.store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, "test-session", 999, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}

	facts := slice.FactIDs()
	if len(facts) < 3 {
		t.Fatalf("fact count = %d, want at least 3 (bash decl+cap, ls decl)", len(facts))
	}
	bashDecl := slice.FactsByID[facts[0]].(shepherdtrace.Record)
	if cmd, _ := bashDecl.Body.Payload["cmd"].(string); cmd != "rm -rf /tmp/probe" {
		t.Errorf("bash declaration payload cmd = %q, want the shell command (the supervision rule's match key)", cmd)
	}
	lsDecl := slice.FactsByID[facts[2]].(shepherdtrace.Record)
	if _, has := lsDecl.Body.Payload["cmd"]; has {
		t.Error("non-bash declaration must not carry a cmd key")
	}
}

// TestTraceMiddleware_PowerShellDeclarationCarriesCmd pins the Windows
// half of the supervision event shape: the shell tool is powershell
// there, and its declarations must carry the same "cmd" key the
// advisory rules match on — otherwise the destructive rule is blind on
// exactly the platform where bash never appears.
func TestTraceMiddleware_PowerShellDeclarationCarriesCmd(t *testing.T) {
	m := newTestTraceMiddleware(t)

	if _, err := m.PostTool(context.Background(), []ToolResult{
		{Name: "powershell", Args: `{"command":"Remove-Item -Recurse /tmp/probe"}`},
	}, &Step{}); err != nil {
		t.Fatalf("PostTool error: %v", err)
	}

	slice, err := m.store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, "test-session", 999, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	facts := slice.FactIDs()
	if len(facts) < 1 {
		t.Fatal("no facts recorded")
	}
	decl := slice.FactsByID[facts[0]].(shepherdtrace.Record)
	if cmd, _ := decl.Body.Payload["cmd"].(string); cmd != "Remove-Item -Recurse /tmp/probe" {
		t.Errorf("powershell declaration payload cmd = %q, want the shell command", cmd)
	}
}
