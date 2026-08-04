package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

func toolCallResponse(id string) *types.ChatResponse {
	return &types.ChatResponse{
		Choices: []types.Choice{{
			Message: types.Message{
				Role: "assistant",
				ToolCalls: []types.ToolCall{{
					ID:   id,
					Type: "function",
					Function: types.ToolCallFn{
						Name:      "read",
						Arguments: `{"path":"/dev/null"}`,
					},
				}},
			},
			FinishReason: "tool_calls",
		}},
	}
}

func textResponse(text string) *types.ChatResponse {
	return &types.ChatResponse{
		Choices: []types.Choice{{
			Message:      types.Message{Role: "assistant", Content: text},
			FinishReason: "stop",
		}},
	}
}

func requestHasNudge(req types.ChatRequest) bool {
	for _, m := range req.Messages {
		if strings.Contains(m.Content, "Budget notice") {
			return true
		}
	}
	return false
}

func lastMessage(req types.ChatRequest) types.Message {
	return req.Messages[len(req.Messages)-1]
}

// MaxToolTurns=2, WrapUpThreshold=1: the nudge must appear on the last tool
// iteration before the strip, be transient (absent from the stripped
// turn's rebuilt request), and carry the correct countdown.
func TestWrapUp_InjectedBeforeStrip(t *testing.T) {
	fp := &fakeProvider{responses: []*types.ChatResponse{
		toolCallResponse("c1"),
		toolCallResponse("c2"),
		textResponse("final report"),
	}}
	loop := &Loop{Config: LoopConfig{SystemPrompt: "You are helpful.",
		MaxLoopCycles:   10,
		MaxToolTurns:    2,
		WrapUpThreshold: 1}, Provider: fp,
		Registry: tools.NewRegistry(),
	}

	resp, err := loop.Run(context.Background(), "do the work")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if resp != "final report" {
		t.Errorf("response = %q", resp)
	}
	if len(fp.requests) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(fp.requests))
	}
	if requestHasNudge(fp.requests[0]) {
		t.Error("iteration 0 must not carry the wrap-up notice")
	}
	if !requestHasNudge(fp.requests[1]) {
		t.Error("iteration 1 (one turn before strip) must carry the wrap-up notice")
	}
	if got := lastMessage(fp.requests[1]).Content; !strings.Contains(got, "1 working turn(s) remain") {
		t.Errorf("countdown wrong: %q", got)
	}
	if requestHasNudge(fp.requests[2]) {
		t.Error("nudge is transient and must not persist into the stripped turn")
	}
	if fp.requests[2].Tools != nil {
		t.Error("iteration 2 must have tools stripped")
	}
}

// A negative WrapUpThreshold disables the notice entirely.
func TestWrapUp_Disabled(t *testing.T) {
	fp := &fakeProvider{responses: []*types.ChatResponse{
		toolCallResponse("c1"),
		toolCallResponse("c2"),
		textResponse("done"),
	}}
	loop := &Loop{Config: LoopConfig{SystemPrompt: "You are helpful.",
		MaxLoopCycles:   10,
		MaxToolTurns:    2,
		WrapUpThreshold: -1}, Provider: fp,
		Registry: tools.NewRegistry(),
	}

	if _, err := loop.Run(context.Background(), "do the work"); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	for i, req := range fp.requests {
		if requestHasNudge(req) {
			t.Errorf("request %d carries a nudge despite WrapUpThreshold=-1", i)
		}
	}
}

// With MaxToolTurns off, the nudge fires before the hard iteration limit so
// the run never ends without a warning.
func TestWrapUp_HardIterationLimit(t *testing.T) {
	fp := &fakeProvider{responses: []*types.ChatResponse{
		toolCallResponse("c1"),
		toolCallResponse("c2"),
		toolCallResponse("c3"),
	}}
	loop := &Loop{Config: LoopConfig{SystemPrompt: "You are helpful.",
		MaxLoopCycles:   3,
		MaxToolTurns:    0,
		WrapUpThreshold: 1}, Provider: fp,
		Registry: tools.NewRegistry(),
	}

	_, err := loop.Run(context.Background(), "run out the clock")
	if err == nil || !strings.Contains(err.Error(), "max iterations") {
		t.Fatalf("expected max iterations error, got %v", err)
	}
	if len(fp.requests) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(fp.requests))
	}
	if requestHasNudge(fp.requests[0]) || requestHasNudge(fp.requests[1]) {
		t.Error("early iterations must not carry the wrap-up notice")
	}
	if !requestHasNudge(fp.requests[2]) {
		t.Error("final iteration before the hard limit must carry the wrap-up notice")
	}
}
