package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/supervision"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// steerInjectTool is a tool whose execution injects supervision guidance
// into the run's steering channel — deterministically mid-run, during
// turn 1's tool phase, so step 2's PrepareStep is the drain point.
type steerInjectTool struct {
	ch chan string
}

func (t *steerInjectTool) Name() string { return "poke" }
func (t *steerInjectTool) Description() string {
	return "pokes the supervisor bridge for tests"
}
func (t *steerInjectTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *steerInjectTool) Execute(ctx context.Context, args string) (string, error) {
	t.ch <- "stop doing that, use the other approach"
	return "poked", nil
}

func steerToolCallResponse(id, name string) *types.ChatResponse {
	args, _ := json.Marshal(map[string]string{})
	return &types.ChatResponse{
		Choices: []types.Choice{{
			Message: types.Message{
				Role: "assistant",
				ToolCalls: []types.ToolCall{{
					ID:   id,
					Type: "function",
					Function: types.ToolCallFn{
						Name:      name,
						Arguments: string(args),
					},
				}},
			},
			FinishReason: "tool_calls",
		}},
		Usage: types.Usage{TotalTokens: 5},
	}
}

// TestSubAgentLoop_SteerChannelDeliversGuidance pins the A2 bridge's
// loop side: guidance injected into the run's steering channel lands as
// a high-priority user message in the NEXT model turn — "[STEER] "
// prefixed, before the model continues — not silently dropped.
func TestSubAgentLoop_SteerChannelDeliversGuidance(t *testing.T) {
	steerCh := make(chan string, supervision.SteerBuffer)
	reg := tools.NewEmptyRegistry()
	reg.Register(&steerInjectTool{ch: steerCh})

	fp := &fakeProvider{responses: []*types.ChatResponse{
		steerToolCallResponse("tc1", "poke"),
		{
			Choices: []types.Choice{{Message: types.Message{Role: "assistant", Content: "done"}, FinishReason: "stop"}},
			Usage:   types.Usage{TotalTokens: 5},
		},
	}}

	loop := NewSubAgentLoop(fp, reg, "test-model", "sys", SubAgentConfig{
		MaxLoopCycles: 5,
		MaxToolTurns:  5,
		SteerCh:       steerCh,
	})

	if _, err := loop.Run(context.Background(), "do the thing"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fp.requests) != 2 {
		t.Fatalf("requests = %d, want 2 (tool turn + final turn)", len(fp.requests))
	}

	var sawSteer bool
	for _, m := range fp.requests[1].Messages {
		if m.Role == "user" && strings.Contains(m.Content, "[STEER] stop doing that, use the other approach") {
			sawSteer = true
		}
	}
	if !sawSteer {
		t.Errorf("the second turn's prompt must carry the injected guidance:\n%v", fp.requests[1].Messages)
	}
}

// TestSubAgentLoop_NoSteerChannelIsUnchanged keeps the default honest:
// without a channel the loop runs exactly as before — no steer
// middleware, no behavior change.
func TestSubAgentLoop_NoSteerChannelIsUnchanged(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	fp := &fakeProvider{responses: []*types.ChatResponse{{
		Choices: []types.Choice{{Message: types.Message{Role: "assistant", Content: "done"}, FinishReason: "stop"}},
		Usage:   types.Usage{TotalTokens: 5},
	}}}
	loop := NewSubAgentLoop(fp, reg, "test-model", "sys", SubAgentConfig{
		MaxLoopCycles: 5,
		MaxToolTurns:  5,
	})
	if _, err := loop.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fp.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(fp.requests))
	}
}
