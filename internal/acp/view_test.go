package acp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/todo"
)

// captureUpdate builds a sender capturing the raw update envelopes.
func captureUpdate(out *[]any) updateSender {
	return func(_ string, u any) {
		*out = append(*out, u)
	}
}

// decodeUpdate unmarshals a captured update for field checks.
func decodeUpdate(t *testing.T, u any) map[string]any {
	t.Helper()
	data, _ := json.Marshal(u)
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("update not valid JSON: %v", err)
	}
	return m
}

// TestView_sessionInfoUpdate pins the title-transition translation: a
// TitleSetEvent becomes one session_info_update with the title and an
// RFC3339 UTC updatedAt, and no other kinds are emitted.
func TestView_sessionInfoUpdate(t *testing.T) {
	var got []any
	v := NewView()
	v.SendTo("sess_x", captureUpdate(&got), &agent.TitleSetEvent{
		Title:     "fix the flaky test",
		UpdatedAt: 1761060000,
	})

	if len(got) != 1 {
		t.Fatalf("updates = %d, want 1", len(got))
	}
	m := decodeUpdate(t, got[0])
	if m["sessionUpdate"] != UpdateSessionInfo {
		t.Fatalf("update variant = %v, want %v", m["sessionUpdate"], UpdateSessionInfo)
	}
	if m["title"] != "fix the flaky test" {
		t.Fatalf("title = %v", m["title"])
	}
	// RFC3339 UTC (the session/list conversion applied to a Unix-seconds
	// event timestamp); computed programmatically, never by hand.
	if want := time.Unix(1761060000, 0).UTC().Format(time.RFC3339); m["updatedAt"] != want {
		t.Fatalf("updatedAt = %v, want %v", m["updatedAt"], want)
	}
}

// TestView_messageChunks checks the agent_message_chunk and
// agent_thought_chunk shapes.
func TestView_messageChunks(t *testing.T) {
	var got []any
	v := NewView()
	v.SendTo("sess_x", captureUpdate(&got), &agent.TokenDeltaEvent{Text: "hello"})
	v.SendTo("sess_x", captureUpdate(&got), &agent.ThinkingEvent{Text: "hmm"})

	m := decodeUpdate(t, got[0])
	if m["sessionUpdate"] != UpdateAgentMessageChunk {
		t.Errorf("first update variant = %v", m["sessionUpdate"])
	}
	content := m["content"].(map[string]any)
	if content["type"] != "text" || content["text"] != "hello" {
		t.Errorf("content = %v", content)
	}
	m = decodeUpdate(t, got[1])
	if m["sessionUpdate"] != UpdateAgentThoughtChunk {
		t.Errorf("second update variant = %v", m["sessionUpdate"])
	}
}

// TestView_toolCallCorrelation checks tool_call then tool_call_update
// share a toolCallId, carry required title/kind, and use only valid
// status enums.
func TestView_toolCallCorrelation(t *testing.T) {
	var got []any
	v := NewView()
	v.SendTo("sess_x", captureUpdate(&got), &agent.ToolStartEvent{ID: 7, Name: "bash", Args: `{"command":"ls"}`})
	v.SendTo("sess_x", captureUpdate(&got), &agent.ToolEndEvent{ID: 7, Name: "bash", Args: `{"command":"ls"}`, Result: "ok\n"})

	first := decodeUpdate(t, got[0])
	if first["sessionUpdate"] != UpdateToolCall {
		t.Fatalf("first = %v, want tool_call", first["sessionUpdate"])
	}
	if v1, ok := first["toolCallId"].(string); !ok || v1 == "" {
		t.Fatalf("toolCallId = %v, want non-empty string", first["toolCallId"])
	}
	if first["status"] != StatusInProgress {
		t.Errorf("initial status = %v, want in_progress", first["status"])
	}
	if title, _ := first["title"].(string); title == "" {
		t.Errorf("tool_call.title missing (required by ACP): %v", first)
	}
	if kind, _ := first["kind"].(string); kind == "" {
		t.Errorf("tool_call.kind missing: %v", first)
	}

	second := decodeUpdate(t, got[1])
	if second["sessionUpdate"] != UpdateToolCallUpdate {
		t.Fatalf("second = %v, want tool_call_update", second["sessionUpdate"])
	}
	if second["toolCallId"] != first["toolCallId"] {
		t.Errorf("toolCallId mismatch: %v vs %v", second["toolCallId"], first["toolCallId"])
	}
	if second["status"] != StatusCompleted {
		t.Errorf("final status = %v, want completed", second["status"])
	}
	contents, _ := second["content"].([]any)
	if len(contents) != 1 {
		t.Fatalf("content blocks = %v", second["content"])
	}
	block := contents[0].(map[string]any)
	if block["type"] != "content" {
		t.Errorf("block type = %v, want content", block["type"])
	}
}

// TestView_toolCallFailed checks the failed status path.
func TestView_toolCallFailed(t *testing.T) {
	var got []any
	v := NewView()
	v.SendTo("sess_x", captureUpdate(&got), &agent.ToolEndEvent{ID: 3, Name: "bash", Error: "boom"})

	m := decodeUpdate(t, got[0])
	if m["status"] != StatusFailed {
		t.Errorf("status = %v, want failed", m["status"])
	}
}

// TestView_doneEventIgnored matches the exhaustive-event contract: the
// turn completion rides the session/prompt response, not DoneEvent.
func TestView_doneEventIgnored(t *testing.T) {
	var got []any
	v := NewView()
	v.SendTo("sess_x", captureUpdate(&got), &agent.DoneEvent{})
	if len(got) != 0 {
		t.Fatalf("DoneEvent produced %d updates", len(got))
	}
}

// TestView_remainingEventsRender covers sub-agent, escalation, and
// compactionsurfaces as agent messages.
func TestView_remainingEventsRender(t *testing.T) {
	var got []any
	v := NewView()
	v.SendTo("s", captureUpdate(&got), &agent.SubAgentStartEvent{Role: "dev", Prompt: "p"})
	v.SendTo("s", captureUpdate(&got), &agent.SubAgentEndEvent{Role: "dev"})
	v.SendTo("s", captureUpdate(&got), &agent.EscalationEvent{Severity: "info", SubAgentRole: "dev", Summary: "s"})
	v.SendTo("s", captureUpdate(&got), &agent.CompactionStartedEvent{BeforeTokens: 1, TargetTokens: 2})
	v.SendTo("s", captureUpdate(&got), &agent.CompactionDoneEvent{BeforeTokens: 1, AfterTokens: 2, Method: "single", SavingsPct: 0.5})
	v.SendTo("s", captureUpdate(&got), &agent.FlushEvent{Content: "x"})
	if len(got) != 6 {
		t.Fatalf("updates = %d, want 6", len(got))
	}
	for i, u := range got {
		m := decodeUpdate(t, u)
		if m["sessionUpdate"] != UpdateAgentMessageChunk {
			t.Errorf("update %d variant = %v, want agent_message_chunk", i, m["sessionUpdate"])
		}
	}
}

// TestView_planShape checks the todo→plan mapping, including status
// clamping for todo statuses outside the ACP enum.
func TestView_planShape(t *testing.T) {
	entries := planEntries([]todo.Item{
		{ID: "1", Content: "first", Status: "in_progress", Priority: "high"},
		{ID: "2", Content: "second", Status: "cancelled"},
	})
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if entries[0].Content != "first" || entries[0].Status != PlanInProgress || entries[0].Priority != PriorityHigh {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	if entries[1].Status != PlanPending {
		t.Errorf("cancelled todo should clamp to pending, got %+v", entries[1])
	}
}
