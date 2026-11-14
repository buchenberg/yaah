package agent

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

func dupTestCall(id, name string) types.ToolCall {
	return types.ToolCall{ID: id, Type: "function", Function: types.ToolCallFn{Name: name, Arguments: `{}`}}
}

func TestDedupeToolCallIDs(t *testing.T) {
	assistantWithCalls := func(calls ...types.ToolCall) types.Message {
		return types.Message{Role: "assistant", ToolCalls: calls}
	}
	gotIDs := func(msg types.Message) []string {
		ids := make([]string, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			ids = append(ids, tc.ID)
		}
		return ids
	}

	t.Run("no tool calls", func(t *testing.T) {
		msg := types.Message{Role: "assistant", Content: "hi"}
		got, renamed := dedupeToolCallIDs(nil, msg)
		if len(got.ToolCalls) != 0 || renamed != nil {
			t.Errorf("expected no-op, got %d calls, renamed %v", len(got.ToolCalls), renamed)
		}
	})

	t.Run("unique ids unchanged", func(t *testing.T) {
		msg := assistantWithCalls(dupTestCall("call_1", "read"), dupTestCall("call_2", "grep"))
		got, renamed := dedupeToolCallIDs(nil, msg)
		if renamed != nil {
			t.Errorf("expected no renames, got %v", renamed)
		}
		if ids := gotIDs(got); ids[0] != "call_1" || ids[1] != "call_2" {
			t.Errorf("ids changed: %v", ids)
		}
	})

	t.Run("duplicate within message", func(t *testing.T) {
		orig := []types.ToolCall{dupTestCall("toolu_x", "grep"), dupTestCall("toolu_x", "read")}
		msg := types.Message{Role: "assistant", ToolCalls: orig}
		got, renamed := dedupeToolCallIDs(nil, msg)

		if orig[0].ID != "toolu_x" || orig[1].ID != "toolu_x" {
			t.Errorf("input slice mutated: %v", orig)
		}
		if len(renamed) != 1 || renamed[0] != "toolu_x" {
			t.Errorf("renamed = %v, want [toolu_x]", renamed)
		}
		ids := gotIDs(got)
		if ids[0] != "toolu_x" || ids[1] != "toolu_x#dup2" {
			t.Errorf("ids = %v, want [toolu_x toolu_x#dup2]", ids)
		}
	})

	t.Run("collision with history", func(t *testing.T) {
		history := []types.Message{assistantWithCalls(dupTestCall("toolu_old", "read"))}
		msg := assistantWithCalls(dupTestCall("toolu_old", "grep"))
		got, renamed := dedupeToolCallIDs(history, msg)
		if len(renamed) != 1 || renamed[0] != "toolu_old" {
			t.Errorf("renamed = %v, want [toolu_old]", renamed)
		}
		if ids := gotIDs(got); ids[0] != "toolu_old#dup2" {
			t.Errorf("ids = %v, want [toolu_old#dup2]", ids)
		}
	})

	t.Run("triple duplicate", func(t *testing.T) {
		msg := assistantWithCalls(dupTestCall("c", "a"), dupTestCall("c", "b"), dupTestCall("c", "d"))
		got, renamed := dedupeToolCallIDs(nil, msg)
		if len(renamed) != 2 {
			t.Errorf("renamed = %v, want 2 entries", renamed)
		}
		ids := gotIDs(got)
		if ids[0] != "c" || ids[1] != "c#dup2" || ids[2] != "c#dup3" {
			t.Errorf("ids = %v, want [c c#dup2 c#dup3]", ids)
		}
	})

	t.Run("fresh id skips taken variant", func(t *testing.T) {
		history := []types.Message{assistantWithCalls(dupTestCall("c", "a"), dupTestCall("c#dup2", "b"))}
		msg := assistantWithCalls(dupTestCall("c", "e"), dupTestCall("c", "f"))
		got, _ := dedupeToolCallIDs(history, msg)
		ids := gotIDs(got)
		if ids[0] != "c#dup3" || ids[1] != "c#dup4" {
			t.Errorf("ids = %v, want [c#dup3 c#dup4]", ids)
		}
	})

	t.Run("resume regenerates sequence id", func(t *testing.T) {
		// DSML recovery restarts its per-client id sequence at zero on
		// resume: the new run can regenerate an id the persisted history
		// already carries.
		history := []types.Message{assistantWithCalls(dupTestCall("dsml_1", "edit"))}
		msg := assistantWithCalls(dupTestCall("dsml_1", "edit"))
		got, renamed := dedupeToolCallIDs(history, msg)
		if len(renamed) != 1 || renamed[0] != "dsml_1" {
			t.Errorf("renamed = %v, want [dsml_1]", renamed)
		}
		if ids := gotIDs(got); ids[0] != "dsml_1#dup2" {
			t.Errorf("ids = %v, want [dsml_1#dup2]", ids)
		}
	})
}

// TestLoop_duplicateToolCallIDsRenamed pins the end-to-end repair: a
// provider response that uses one tool_use id for two calls must still
// execute both, and the conversation (in memory, in the follow-up
// request, and persisted for a later resume) must carry distinct ids —
// the Anthropic Messages API rejects a request with two tool_result
// blocks sharing one id ("each tool_use must have a single result").
func TestLoop_duplicateToolCallIDsRenamed(t *testing.T) {
	tmp := t.TempDir()
	db, err := memory.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	sessionID := "sess-dupids"
	db.CreateSession(memory.Session{
		ID: sessionID, StartedAt: time.Now().Unix(), CWD: "/tmp", Model: "test",
	})

	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			{
				Choices: []types.Choice{{
					Message: types.Message{
						Role: "assistant",
						ToolCalls: []types.ToolCall{
							dupTestCall("call_dup", "noop"),
							dupTestCall("call_dup", "noop"),
						},
					},
					FinishReason: "tool_calls",
				}},
			},
			{
				Choices: []types.Choice{{
					Message:      types.Message{Role: "assistant", Content: "done"},
					FinishReason: "stop",
				}},
			},
		},
	}

	var cnt int
	var mu sync.Mutex
	reg := tools.NewRegistry()
	reg.Register(&fakeTool{name: "noop", result: "ok", callCnt: &cnt, mu: &mu})

	loop := &Loop{
		Provider:  fp,
		Registry:  reg,
		Persister: NewSessionPersister(db, nil, sessionID),
		Config: LoopConfig{
			SystemPrompt:  "You are helpful.",
			SessionID:     sessionID,
			MaxLoopCycles: 5,
		},
	}

	resp, err := loop.Run(context.Background(), "run the same tool twice")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if resp != "done" {
		t.Errorf("response = %q", resp)
	}

	// Both calls executed despite sharing the provider-assigned id.
	mu.Lock()
	executed := cnt
	mu.Unlock()
	if executed != 2 {
		t.Errorf("expected both duplicate-id calls to execute, got %d", executed)
	}

	// In-memory history: the second occurrence was renamed.
	assertUniqueToolIDs(t, loop.State.Messages)
	assertToolCallContract(t, loop.State.Messages)
	var gotIDs []string
	for _, m := range loop.State.Messages {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				gotIDs = append(gotIDs, tc.ID)
			}
		}
	}
	if len(gotIDs) != 2 || gotIDs[0] != "call_dup" || gotIDs[1] != "call_dup#dup2" {
		t.Errorf("assistant tool call ids = %v, want [call_dup call_dup#dup2]", gotIDs)
	}

	// The follow-up request replays the same unique pairing.
	if len(fp.requests) != 2 {
		t.Fatalf("expected 2 provider requests, got %d", len(fp.requests))
	}
	assertUniqueToolIDs(t, fp.requests[1].Messages)
	assertToolCallContract(t, fp.requests[1].Messages)

	// The renamed ids persist, so a resumed session replays a valid
	// history instead of regenerating the collision.
	msgs, err := db.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("GetMessages() error: %v", err)
	}
	if len(msgs) != 6 {
		t.Fatalf("expected 6 persisted messages (system, user, assistant, tool, tool, assistant), got %d", len(msgs))
	}
	if !strings.Contains(msgs[2].ToolCalls, "call_dup#dup2") {
		t.Errorf("persisted assistant ToolCalls missing renamed id: %s", msgs[2].ToolCalls)
	}
	if msgs[3].ToolCallID != "call_dup" {
		t.Errorf("msgs[3].ToolCallID = %q, want call_dup", msgs[3].ToolCallID)
	}
	if msgs[4].ToolCallID != "call_dup#dup2" {
		t.Errorf("msgs[4].ToolCallID = %q, want call_dup#dup2", msgs[4].ToolCallID)
	}
}

// assertUniqueToolIDs fails the test when any tool call id is declared
// more than once, when any tool result id appears more than once, or when
// a tool result has no matching call — the shapes the Anthropic Messages
// API rejects with 400 "each tool_use must have a single result" /
// "tool_use ids were found without tool_result blocks".
func assertUniqueToolIDs(t *testing.T, messages []types.Message) {
	t.Helper()
	calls := make(map[string]int)
	results := make(map[string]int)
	for _, m := range messages {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				calls[tc.ID]++
			}
		}
		if m.Role == "tool" {
			results[m.ToolCallID]++
		}
	}
	for id, n := range calls {
		if n > 1 {
			t.Errorf("tool call id %q declared %d times", id, n)
		}
	}
	for id, n := range results {
		if n > 1 {
			t.Errorf("tool result id %q appears %d times", id, n)
		}
		if calls[id] == 0 {
			t.Errorf("tool result id %q has no matching call", id)
		}
	}
}
