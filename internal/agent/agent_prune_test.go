package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// pruneBig is a tool-result body small enough to escape ToolResultMaxLen
// truncation (8192) yet large enough (~1500 tokens) to exceed the test
// Pruner's 1000-token protect window.
const pruneBig = 6000

// prunableLoop builds a Loop wired for soft-prune integration tests: an echo
// tool returning a large result, and a Pruner whose protect window (1000
// tokens) is smaller than one result so older results get pruned.
func prunableLoop(t *testing.T, fp *fakeProvider) *Loop {
	t.Helper()
	big := strings.Repeat("payload-", pruneBig/8)
	reg := tools.NewRegistry()
	reg.Register(&fakeTool{name: "echo", result: big})
	return &Loop{Config: LoopConfig{SystemPrompt: "test",
		MaxIterations: 10}, Provider: fp,
		Registry: reg,

		CtxMgr: &ContextManager{
			Pruner: pipeline.NewPruner(pipeline.PruneConfig{
				ProtectTokens: 1000,
				MinReclaim:    10,
				MinTurns:      1,
			}),
		},
	}
}

// oldTurn pre-populates an earlier turn whose tool result becomes eligible
// for pruning once Run appends a fresh user message and a new tool round.
func oldTurn() []types.Message {
	big := strings.Repeat("payload-", pruneBig/8)
	return []types.Message{
		types.SystemMsg("test"),
		types.UserMsg("first task"),
		types.AssistantMsg("", []types.ToolCall{{
			ID: "call_old", Type: "function", Function: types.ToolCallFn{Name: "echo", Arguments: "{}"},
		}}),
		types.ToolResultMsg("call_old", "echo", big),
	}
}

// TestLoop_Pruner_RequestSeesStubs: after a fresh tool round marks the older
// tool result, the NEXT provider request must carry a stub for the old one
// while keeping the freshly-produced result in full.
func TestLoop_Pruner_RequestSeesStubs(t *testing.T) {
	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			// Turn 0: call echo (produces the fresh result).
			{Choices: []types.Choice{{
				Message: types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{
					ID: "call_new", Type: "function", Function: types.ToolCallFn{Name: "echo", Arguments: "{}"},
				}}},
				FinishReason: "tool_calls",
			}}},
			// Turn 1: final answer.
			{Choices: []types.Choice{{
				Message: types.Message{Role: "assistant", Content: "done"}, FinishReason: "stop",
			}}},
		},
	}
	loop := prunableLoop(t, fp)
	loop.State.Messages = oldTurn()

	if _, err := loop.Run(context.Background(), "do work"); err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// The second request (turn 1) is built AFTER turn 0's PostTool marked the
	// old result. Its messages must stub call_old and keep call_new in full.
	if len(fp.requests) < 2 {
		t.Fatalf("expected at least 2 provider requests, got %d", len(fp.requests))
	}
	final := fp.requests[1].Messages

	var oldContent, newContent string
	var oldSeen, newSeen bool
	for _, m := range final {
		if m.Role == "tool" && m.ToolCallID == "call_old" {
			oldContent, oldSeen = m.Content, true
		}
		if m.Role == "tool" && m.ToolCallID == "call_new" {
			newContent, newSeen = m.Content, true
		}
	}
	if !oldSeen || !newSeen {
		t.Fatalf("request missing tool messages: old=%v new=%v", oldSeen, newSeen)
	}
	if !strings.Contains(oldContent, "pruned") {
		t.Errorf("old tool result should be stubbed, got %q...", truncForTest(oldContent, 60))
	}
	if strings.Contains(newContent, "pruned") {
		t.Errorf("fresh tool result should be full, got stubbed: %q...", truncForTest(newContent, 60))
	}
	if len(newContent) != pruneBig {
		t.Errorf("fresh tool result length = %d, want %d", len(newContent), pruneBig)
	}
}

// TestLoop_Pruner_MessagesIntact: the Loop's retained history must still hold
// the full original content for every tool result (DB-equivalent invariant).
func TestLoop_Pruner_MessagesIntact(t *testing.T) {
	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			{Choices: []types.Choice{{
				Message: types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{
					ID: "call_new", Type: "function", Function: types.ToolCallFn{Name: "echo", Arguments: "{}"},
				}}},
				FinishReason: "tool_calls",
			}}},
			{Choices: []types.Choice{{
				Message: types.Message{Role: "assistant", Content: "done"}, FinishReason: "stop",
			}}},
		},
	}
	loop := prunableLoop(t, fp)
	loop.State.Messages = oldTurn()

	if _, err := loop.Run(context.Background(), "do work"); err != nil {
		t.Fatalf("Run error: %v", err)
	}

	for _, m := range loop.State.Messages {
		if m.Role == "tool" && (m.ToolCallID == "call_old" || m.ToolCallID == "call_new") {
			if len(m.Content) != pruneBig {
				t.Errorf("retained message %q content was mutated: len=%d want %d (stubbed? %v)",
					m.ToolCallID, len(m.Content), pruneBig, strings.Contains(m.Content, "pruned"))
			}
		}
	}
}

// TestLoop_Pruner_ToolCallIDLinkagePreserved: every assistant tool_calls[].id
// must still have a matching tool message in the request — soft-prune never
// removes messages, so the provider cannot 400 on linkage.
func TestLoop_Pruner_ToolCallIDLinkagePreserved(t *testing.T) {
	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			{Choices: []types.Choice{{
				Message: types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{
					ID: "call_new", Type: "function", Function: types.ToolCallFn{Name: "echo", Arguments: "{}"},
				}}},
				FinishReason: "tool_calls",
			}}},
			{Choices: []types.Choice{{
				Message: types.Message{Role: "assistant", Content: "done"}, FinishReason: "stop",
			}}},
		},
	}
	loop := prunableLoop(t, fp)
	loop.State.Messages = oldTurn()

	if _, err := loop.Run(context.Background(), "do work"); err != nil {
		t.Fatalf("Run error: %v", err)
	}

	for ri, req := range fp.requests {
		toolIDs := map[string]bool{}
		for _, m := range req.Messages {
			if m.Role == "tool" {
				toolIDs[m.ToolCallID] = true
			}
		}
		for _, m := range req.Messages {
			for _, tc := range m.ToolCalls {
				if !toolIDs[tc.ID] {
					t.Errorf("request %d: assistant tool_call id %q has no matching tool message", ri, tc.ID)
				}
			}
		}
	}
}

// TestLoop_Pruner_ResetOnCompaction: when compaction rebuilds l.Messages,
// the Pruner set must be cleared so previously-pruned IDs are re-evaluated.
func TestLoop_Pruner_ResetOnCompaction(t *testing.T) {
	// A fake provider that returns a non-empty summary for compaction calls.
	compactFP := &fakeProvider{
		responses: []*types.ChatResponse{
			{Choices: []types.Choice{{
				Message: types.Message{Role: "assistant", Content: "summary of old messages"}, FinishReason: "stop",
			}}},
		},
	}
	loop := &Loop{Config: LoopConfig{SystemPrompt: "test",
		Model:         "test",
		ContextWindow: 500}, Provider: compactFP,

		// small → compaction fires readily
		CtxMgr: &ContextManager{
			Pruner: pipeline.NewPruner(pipeline.PruneConfig{ProtectTokens: 1, MinReclaim: 1, MinTurns: 1}),
		},
	}

	// Pre-mark an ID so we can observe the reset.
	markMsgs := []types.Message{
		types.UserMsg("u"),
		types.AssistantMsg("", []types.ToolCall{{ID: "old_call", Type: "function", Function: types.ToolCallFn{Name: "read"}}}),
		types.ToolResultMsg("old_call", "read", strings.Repeat("x", 100000)),
		types.UserMsg("end"),
	}
	loop.CtxMgr.Pruner.Mark(markMsgs, "setup")
	if !loop.CtxMgr.Pruner.IsPruned("old_call") {
		t.Fatalf("precondition: old_call should be marked before compaction")
	}

	// Build a large history that forces compaction: system + 13 big user msgs.
	loop.State.Messages = []types.Message{types.SystemMsg("test")}
	for i := 0; i < 13; i++ {
		loop.State.Messages = append(loop.State.Messages, types.UserMsg(strings.Repeat("y", 10000)))
	}

	loop.compactContext(context.Background(), 0.5)

	if loop.CtxMgr.Pruner.IsPruned("old_call") {
		t.Errorf("Pruner should be reset after compaction rebuilt messages")
	}
}

// TestLoop_Pruner_DisabledViaPipeline: with soft_prune in PipelineDisabled,
// no marking occurs and Filter is identity — behaviour matches pre-change.
func TestLoop_Pruner_DisabledViaPipeline(t *testing.T) {
	big := strings.Repeat("payload-", pruneBig/8)
	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			{Choices: []types.Choice{{
				Message: types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{
					ID: "call_new", Type: "function", Function: types.ToolCallFn{Name: "echo", Arguments: "{}"},
				}}},
				FinishReason: "tool_calls",
			}}},
			{Choices: []types.Choice{{
				Message: types.Message{Role: "assistant", Content: "done"}, FinishReason: "stop",
			}}},
		},
	}
	reg := tools.NewRegistry()
	reg.Register(&fakeTool{name: "echo", result: big})
	loop := &Loop{Config: LoopConfig{SystemPrompt: "test",
		MaxIterations:    10,
		PipelineDisabled: []string{"soft_prune"}}, Provider: fp,
		Registry: reg,
	}
	loop.State.Messages = oldTurn()

	if _, err := loop.Run(context.Background(), "do work"); err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// With soft_prune disabled, the Pruner set must be empty.
	if s := loop.CtxMgr.Pruner.Stats(); s.TotalMarked != 0 {
		t.Errorf("disabled soft_prune should leave the pruned set empty, got TotalMarked=%d", s.TotalMarked)
	}
	// And every tool result in the final request must be the full content.
	if len(fp.requests) == 0 {
		t.Fatal("expected at least one provider request")
	}
	for _, m := range fp.requests[len(fp.requests)-1].Messages {
		if m.Role == "tool" && (m.ToolCallID == "call_old" || m.ToolCallID == "call_new") {
			if strings.Contains(m.Content, "pruned") {
				t.Errorf("disabled soft_prune should not stub content for %q", m.ToolCallID)
			}
		}
	}
}

func truncForTest(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
