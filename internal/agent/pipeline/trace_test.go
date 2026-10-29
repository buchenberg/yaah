package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
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

func TestTraceMiddleware_MetaRidesDeclaration(t *testing.T) {
	m := newTestTraceMiddleware(t)
	results := []ToolResult{{
		Name: "write",
		Args: `{"content":"hi","filePath":"/tmp/x"}`,
		Meta: map[string]any{"file_existed": false},
	}}

	if _, err := m.PostTool(context.Background(), results, &Step{}); err != nil {
		t.Fatalf("PostTool error: %v", err)
	}

	slice, err := m.store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, "test-session", 999, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	factIDs := slice.FactIDs()
	if len(factIDs) != 2 {
		t.Fatalf("fact count = %d, want 2", len(factIDs))
	}
	rec, ok := slice.FactsByID[factIDs[0]].(shepherdtrace.Record)
	if !ok {
		t.Fatal("declaration should be a Record")
	}
	if rec.Body.Payload["file_existed"] != false {
		t.Errorf("declaration payload file_existed = %v, want false", rec.Body.Payload["file_existed"])
	}
}

func recordPayload(t *testing.T, slice shepherdtrace.Slice, kind, schema string) map[string]any {
	t.Helper()
	for _, id := range slice.FactIDs() {
		rec, ok := slice.FactsByID[id].(shepherdtrace.Record)
		if !ok {
			continue
		}
		if rec.View.KindLabel == kind && rec.Envelope.SchemaRef == schema {
			return rec.Body.Payload
		}
	}
	t.Fatalf("no %s fact with schema %s", kind, schema)
	return nil
}

func TestTraceMiddleware_RecordsContentFactsAndRebuilds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.sqlite")
	store, err := shepherdtrace.NewSQLiteTraceStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	owner := "sess-content"
	m := NewShepherdTraceMiddleware(store, owner)
	m.SetTurnContext("turn-1")
	m.StartTurn(context.Background(), 0, "test-model", "read the file")
	m.RecordSessionContext(context.Background(), TraceSessionContext{
		SystemPrompt: "you are a test agent",
		CWD:          "/tmp/work",
		Model:        "test-model",
		Provider:     "test-provider",
		ToolNames:    []string{"read", "bash"},
	})
	m.RecordAssistant(context.Background(), &types.Message{
		Role:               "assistant",
		Content:            "I will read it.",
		ReasoningContent:   "need the file",
		ReasoningSignature: "sig-123",
		ToolCalls: []types.ToolCall{{
			ID:   "call-1",
			Type: "function",
			Function: types.ToolCallFn{
				Name:      "read",
				Arguments: `{"file":"a.go"}`,
			},
		}},
	}, 0)
	if _, err := m.PostTool(context.Background(), []ToolResult{{
		Name:   "read",
		Args:   `{"file":"a.go"}`,
		Result: "package main\n",
		CallID: "call-1",
	}}, &Step{}); err != nil {
		t.Fatalf("PostTool: %v", err)
	}
	m.EndTurn(context.Background(), 0, 10, 20)

	slice, err := store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, owner, 999, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	assistant := recordPayload(t, slice, "assistant:message", "yaah.message.assistant.v1")
	if assistant["content"] != "I will read it." {
		t.Errorf("content = %v", assistant["content"])
	}
	if assistant["reasoning_signature"] != "sig-123" {
		t.Errorf("signature = %v", assistant["reasoning_signature"])
	}
	toolResult := recordPayload(t, slice, "read:result", "yaah.tool.read.v1.applied")
	if toolResult["output"] != "package main\n" || toolResult["tool_call_id"] != "call-1" {
		t.Errorf("tool result payload = %#v", toolResult)
	}
	ctxPayload := recordPayload(t, slice, "session:context", "yaah.session.context.v1")
	if ctxPayload["system_prompt"] != "you are a test agent" || ctxPayload["cwd"] != "/tmp/work" {
		t.Errorf("session context = %#v", ctxPayload)
	}

	transcript := RebuildTranscriptFromSlice(slice, owner)
	if transcript.SystemPrompt != "you are a test agent" {
		t.Errorf("rebuilt system prompt = %q", transcript.SystemPrompt)
	}
	if len(transcript.Messages) != 4 {
		t.Fatalf("messages = %d (%#v), want system+user+assistant+tool", len(transcript.Messages), transcript.Messages)
	}
	if transcript.Messages[0].Role != "system" || transcript.Messages[1].Content != "read the file" {
		t.Errorf("prefix = %#v", transcript.Messages[:2])
	}
	if got := transcript.Messages[2]; got.Role != "assistant" || got.ReasoningSignature != "sig-123" || len(got.ToolCalls) != 1 {
		t.Errorf("assistant = %#v", got)
	}
	if got := transcript.Messages[3]; got.Role != "tool" || got.ToolCallID != "call-1" || got.Content != "package main\n" {
		t.Errorf("tool = %#v", got)
	}

	// The store must be readable from a fresh handle: this is the
	// after-process-exit proof, not just an in-memory reconstruction.
	if err := store.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, err := shepherdtrace.NewSQLiteTraceStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close(context.Background())
	again, err := RebuildTranscript(context.Background(), reopened, owner)
	if err != nil {
		t.Fatalf("rebuild after reopen: %v", err)
	}
	if len(again.Messages) != 4 || again.Messages[3].Content != "package main\n" {
		t.Fatalf("after reopen = %#v", again.Messages)
	}
}

func TestTraceMiddleware_ContentCaps(t *testing.T) {
	m := newTestTraceMiddleware(t)
	big := strings.Repeat("x", tracePayloadMaxBytes+500)
	m.RecordAssistant(context.Background(), &types.Message{Role: "assistant", Content: big}, 0)
	m.RecordSessionContext(context.Background(), TraceSessionContext{SystemPrompt: big, CWD: "/tmp"})
	slice, err := m.store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, "test-session", 999, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	assistant := recordPayload(t, slice, "assistant:message", "yaah.message.assistant.v1")
	if assistant["content_truncated"] != true {
		t.Errorf("assistant truncation flag = %v", assistant["content_truncated"])
	}
	if got := fmt.Sprint(assistant["content_bytes"]); got != fmt.Sprint(len(big)) {
		t.Errorf("content_bytes = %v, want %d", got, len(big))
	}
	ctxPayload := recordPayload(t, slice, "session:context", "yaah.session.context.v1")
	if ctxPayload["system_prompt_truncated"] != true {
		t.Errorf("system prompt truncation flag = %v", ctxPayload["system_prompt_truncated"])
	}
}

func TestTraceMiddleware_ToolOutputSpillReference(t *testing.T) {
	m := newTestTraceMiddleware(t)
	result := "partial\n...[output truncated at 1 lines / 999 bytes — full output at /tmp/spill/1-tool.txt. To read portions, use the read tool]..."
	if _, err := m.PostTool(context.Background(), []ToolResult{{Name: "read", Args: `{}`, Result: result, CallID: "c1"}}, &Step{}); err != nil {
		t.Fatalf("PostTool: %v", err)
	}
	slice, err := m.store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, "test-session", 99, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	payload := recordPayload(t, slice, "read:result", "yaah.tool.read.v1.applied")
	if payload["output_spill"] != "/tmp/spill/1-tool.txt" {
		t.Errorf("output_spill = %v", payload["output_spill"])
	}
	if payload["output_truncated"] != true {
		t.Errorf("output_truncated = %v", payload["output_truncated"])
	}
}

func TestTraceMiddleware_StampsRetainedContext(t *testing.T) {
	m := newTestTraceMiddleware(t)
	m.retainedContext = &shepherdtrace.RetainedContext{
		SubstrateRef:          "yaah.substrate.worktree.v1",
		Containment:           shepherdtrace.ContainContained,
		CapabilityWitnessRefs: []string{"yaah.tool.read.v1"},
		ActiveBindingRefs:     []string{"role:developer"},
	}
	if _, err := m.PostTool(context.Background(), []ToolResult{{Name: "read", Args: `{}`, Result: "ok", CallID: "c1"}}, &Step{}); err != nil {
		t.Fatalf("PostTool: %v", err)
	}
	slice, err := m.store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, "test-session", 99, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, id := range slice.FactIDs() {
		ref := slice.FactsByID[id].GetView().ContextRef
		if ref == "" {
			t.Fatalf("fact %s has no context ref", id)
		}
		rc, ok := slice.ContextsByID[ref]
		if !ok {
			t.Fatalf("context %s missing", ref)
		}
		if rc.SubstrateRef != "yaah.substrate.worktree.v1" || rc.Containment != shepherdtrace.ContainContained {
			t.Errorf("context = %#v", rc)
		}
	}
}
