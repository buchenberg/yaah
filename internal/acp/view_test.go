package acp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/todo"
	"github.com/buchenberg/yaah/internal/types"
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

// TestView_doneUsageUpdate checks the end-of-turn usage translation:
// the occupancy comes from LastPromptTokensInclusive (falling back to
// the char/4 estimate), the window is the size, and the _meta bag
// carries the engine's tally, finish reason, and response model.
func TestView_doneUsageUpdate(t *testing.T) {
	var got []any
	v := NewView()
	v.SendTo("sess_x", captureUpdate(&got), &agent.DoneEvent{
		ContextTokens:             120,
		ContextWindow:             1000,
		LastPromptTokensInclusive: 800,
		FinishReason:              "end_of_turn",
		ResponseModel:             "anthropic/claude-x",
		Usage:                     types.Usage{PromptTokens: 800, TotalTokens: 830},
	})
	if len(got) != 1 {
		t.Fatalf("updates = %d, want 1", len(got))
	}
	m := decodeUpdate(t, got[0])
	if m["sessionUpdate"] != UpdateUsageUpdate {
		t.Fatalf("variant = %v, want usage_update", m["sessionUpdate"])
	}
	if m["used"] != float64(800) || m["size"] != float64(1000) {
		t.Fatalf("occupancy = %v/%v, want 800/1000", m["used"], m["size"])
	}
	meta, ok := m["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("_meta = %v, want object", m["_meta"])
	}
	if meta["promptTokens"] != float64(800) || meta["totalTokens"] != float64(830) {
		t.Fatalf("token tally = %v", meta)
	}
	if meta["finishReason"] != "end_of_turn" || meta["responseModel"] != "anthropic/claude-x" {
		t.Fatalf("finish/model = %v", meta)
	}
	if _, present := meta["completionTokens"]; present {
		t.Fatalf("zero counts must be omitted: %v", meta)
	}
}

// TestView_diffContentForEditTools checks that file-modifying calls
// carry diff content blocks and jump-to-file locations: write gets an
// oldText-less diff for its new file, edit derives one diff per
// replacement (single or edits[]), and non-edit tools are untouched.
func TestView_diffContentForEditTools(t *testing.T) {
	var got []any
	v := NewView()
	v.SendTo("s", captureUpdate(&got), &agent.ToolStartEvent{
		ID: 1, Name: "edit", RawArgs: `{"filePath":"/repo/a.go","oldString":"x","newString":"y"}`,
	})
	v.SendTo("s", captureUpdate(&got), &agent.ToolEndEvent{
		ID: 1, Name: "edit", RawArgs: `{"filePath":"/repo/a.go","oldString":"x","newString":"y"}`, Result: "ok",
	})
	v.SendTo("s", captureUpdate(&got), &agent.ToolEndEvent{
		ID: 2, Name: "write", RawArgs: `{"filePath":"/repo/b.txt","content":"hi"}`, Result: "ok",
	})
	v.SendTo("s", captureUpdate(&got), &agent.ToolEndEvent{
		ID: 3, Name: "edit", RawArgs: `{"filePath":"/repo/c.go","edits":[{"oldString":"a","newString":"b"},{"oldString":"c","newString":"d"}]}`, Result: "ok",
	})
	v.SendTo("s", captureUpdate(&got), &agent.ToolEndEvent{
		ID: 4, Name: "bash", RawArgs: `{"command":"ls"}`, Result: "ok",
	})

	// tool_call start: locations only.
	start := decodeUpdate(t, got[0])
	locs, _ := start["locations"].([]any)
	if len(locs) != 1 || locs[0].(map[string]any)["path"] != "/repo/a.go" {
		t.Fatalf("start locations = %v", start["locations"])
	}
	// Edit completion: diff first, then the result content.
	end := decodeUpdate(t, got[1])
	blocks, _ := end["content"].([]any)
	if len(blocks) != 2 || blocks[0].(map[string]any)["type"] != "diff" {
		t.Fatalf("edit blocks = %v", end["content"])
	}
	// The schema's diff variant is flattened: path/oldText/newText sit
	// next to the type discriminator, never under a nested "diff" key.
	df := blocks[0].(map[string]any)
	if df["path"] != "/repo/a.go" || df["newText"] != "y" {
		t.Fatalf("diff = %v", df)
	}
	if _, nested := df["diff"]; nested {
		t.Fatalf("diff payload must be flat, not nested: %v", df)
	}
	if df["oldText"] != "x" {
		t.Fatalf("oldText = %v, want x", df["oldText"])
	}
	// Write completion: no oldText (new file), full newText.
	write := decodeUpdate(t, got[2])
	wblocks, _ := write["content"].([]any)
	if len(wblocks) != 2 || wblocks[0].(map[string]any)["type"] != "diff" {
		t.Fatalf("write blocks = %v", write["content"])
	}
	wdiff := wblocks[0].(map[string]any)
	if _, present := wdiff["oldText"]; present {
		t.Fatalf("write diff must omit oldText (new file): %v", wdiff)
	}
	if wdiff["newText"] != "hi" {
		t.Fatalf("write newText = %v", wdiff["newText"])
	}
	// Multi-edit: one diff per replacement.
	multi := decodeUpdate(t, got[3])
	mblocks, _ := multi["content"].([]any)
	if len(mblocks) != 3 {
		t.Fatalf("multi-edit blocks = %v", multi["content"])
	}
	for i, b := range mblocks[:2] {
		if b.(map[string]any)["type"] != "diff" {
			t.Fatalf("multi-edit block %d = %v", i, b)
		}
	}
	// Non-edit tools carry no diff and no locations.
	other := decodeUpdate(t, got[4])
	if other["locations"] != nil {
		t.Fatalf("bash locations = %v, want none", other["locations"])
	}
	for _, b := range other["content"].([]any) {
		if b.(map[string]any)["type"] != "content" {
			t.Fatalf("bash block type = %v, want content", b.(map[string]any)["type"])
		}
	}
}

// TestView_diffEmptyNewText stays on the wire checks that a diff whose
// new content is empty (a deleted block) still transmits required
// newText, per the schema's required string.
func TestView_diffEmptyNewTextStillOnWire(t *testing.T) {
	var got []any
	v := NewView()
	v.SendTo("s", captureUpdate(&got), &agent.ToolEndEvent{
		ID: 1, Name: "edit", RawArgs: `{"filePath":"/repo/a.go","oldString":"gone\n","newString":""}`, Result: "ok",
	})
	m := decodeUpdate(t, got[0])
	blocks, _ := m["content"].([]any)
	if len(blocks) != 2 || blocks[0].(map[string]any)["type"] != "diff" {
		t.Fatalf("blocks = %v", m["content"])
	}
	df := blocks[0].(map[string]any)
	raw, err := json.Marshal(df)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"newText":""`) {
		t.Fatalf("empty newText dropped (must transmit required field): %s", raw)
	}
}

// TestView_diffInvalidArgsTolerated checks that unparsable raw args
// degrade to the plain content block without a diff.
func TestView_diffInvalidArgsTolerated(t *testing.T) {
	var got []any
	v := NewView()
	v.SendTo("s", captureUpdate(&got), &agent.ToolEndEvent{ID: 1, Name: "edit", RawArgs: "not json", Result: "ok"})
	m := decodeUpdate(t, got[0])
	blocks, _ := m["content"].([]any)
	if len(blocks) != 1 || blocks[0].(map[string]any)["type"] != "content" {
		t.Fatalf("blocks = %v", m["content"])
	}
}

// TestView_remainingEventsRender covers sub-agent, escalation, and
// compaction surfaces as agent messages.
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
