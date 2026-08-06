package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// TestLoop_inlineLimitKeepsToolCallContract verifies that when the model
// emits more tool calls than MaxInlineToolsPerTurn, the history stays
// provider-valid: every tool_call_id gets a tool result, results follow
// the assistant message directly, and no user/system message is wedged
// in between. Violating this triggers a provider 400 ("An assistant
// message with 'tool_calls' must be followed by tool messages").
func TestLoop_inlineLimitKeepsToolCallContract(t *testing.T) {
	calls := make([]types.ToolCall, 0, 4)
	for _, id := range []string{"call_1", "call_2", "call_3", "call_4"} {
		calls = append(calls, types.ToolCall{
			ID:   id,
			Type: "function",
			Function: types.ToolCallFn{
				Name:      "noop",
				Arguments: `{}`,
			},
		})
	}

	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			{
				Choices: []types.Choice{{
					Message:      types.Message{Role: "assistant", ToolCalls: calls},
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
		Provider: fp,
		Registry: reg,
		Config: LoopConfig{
			SystemPrompt:          "You are helpful.",
			MaxLoopCycles:         5,
			MaxInlineToolsPerTurn: 2,
		},
	}

	resp, err := loop.Run(context.Background(), "run four tools")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if resp != "done" {
		t.Errorf("response = %q", resp)
	}

	mu.Lock()
	executed := cnt
	mu.Unlock()
	if executed != 2 {
		t.Errorf("expected 2 calls executed (limit), got %d", executed)
	}

	assertToolCallContract(t, loop.State.Messages)

	if len(fp.requests) != 2 {
		t.Fatalf("expected 2 provider requests, got %d", len(fp.requests))
	}
	assertToolCallContract(t, fp.requests[1].Messages)

	droppedResults := 0
	for _, m := range fp.requests[1].Messages {
		if m.Role == "tool" && strings.Contains(m.Content, "[dropped") {
			droppedResults++
		}
	}
	if droppedResults != 2 {
		t.Errorf("expected 2 synthesized dropped-call results in request, got %d", droppedResults)
	}
}

// assertToolCallContract fails the test if any assistant message with
// tool calls is not immediately followed by tool messages covering every
// tool_call_id.
func assertToolCallContract(t *testing.T, messages []types.Message) {
	t.Helper()
	for i, m := range messages {
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		want := make(map[string]bool, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			want[tc.ID] = true
		}
		for j := i + 1; j < len(messages) && len(want) > 0; j++ {
			next := messages[j]
			if next.Role != "tool" {
				t.Fatalf("assistant tool_calls at %d followed by %q message at %d before all tool results arrived", i, next.Role, j)
			}
			delete(want, next.ToolCallID)
		}
		if len(want) > 0 {
			missing := make([]string, 0, len(want))
			for id := range want {
				missing = append(missing, id)
			}
			t.Fatalf("assistant tool_calls at %d missing tool results for %v", i, missing)
		}
	}
}
