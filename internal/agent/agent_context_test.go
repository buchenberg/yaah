package agent

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestPreflightTokens_emptyMessages(t *testing.T) {
	got := preflightTokens(nil, nil, defaultEstimateFactor)
	if got != 0 {
		t.Errorf("preflightTokens(nil, nil) = %d, want 0", got)
	}
}

func TestPreflightTokens_messagesOnly(t *testing.T) {
	msgs := []types.Message{
		types.UserMsg("hello world"),
	}
	got := preflightTokens(msgs, nil, defaultEstimateFactor)
	if got <= 0 {
		t.Errorf("preflightTokens with messages = %d, want > 0", got)
	}
}

func TestPreflightTokens_withTools(t *testing.T) {
	msgs := []types.Message{
		types.UserMsg("hello"),
	}
	tools := []types.ToolDef{
		{
			Type: "function",
			Function: types.ToolFn{
				Name:        "read",
				Description: "Read a file from disk",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
			},
		},
	}
	withTools := preflightTokens(msgs, tools, defaultEstimateFactor)
	withoutTools := preflightTokens(msgs, nil, defaultEstimateFactor)
	if withTools <= withoutTools {
		t.Errorf("preflightTokens with tools (%d) should be > without tools (%d)", withTools, withoutTools)
	}
}

func TestPreflightTokens_1_3xMultiplier(t *testing.T) {
	msgs := []types.Message{
		types.UserMsg("hello world this is a test message with some content"),
	}
	raw := float64(messageTokens(msgs[0]))
	got := preflightTokens(msgs, nil, defaultEstimateFactor)
	expected := int(math.Ceil(raw * defaultEstimateFactor))
	if got != expected {
		t.Errorf("preflightTokens = %d, want %d (raw=%v * factor=%v)", got, expected, raw, defaultEstimateFactor)
	}
}

func TestPreflightTokens_customFactor(t *testing.T) {
	msgs := []types.Message{
		types.UserMsg("hello"),
	}
	f1x := preflightTokens(msgs, nil, 1.0)
	f2x := preflightTokens(msgs, nil, 2.0)
	if f2x <= f1x {
		t.Errorf("2x factor (%d) should be > 1x factor (%d)", f2x, f1x)
	}
}

func TestPreflightTokens_toolEstimate(t *testing.T) {
	tools := []types.ToolDef{
		{
			Type: "function",
			Function: types.ToolFn{
				Name:        "grep",
				Description: "Search file contents using regular expressions",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"}}}`),
			},
		},
	}
	got := preflightTokens(nil, tools, 1.0)
	if got <= 0 {
		t.Errorf("preflightTokens for tools = %d, want > 0", got)
	}
}

func TestIsContinuation_noMessages(t *testing.T) {
	if isContinuation(nil) {
		t.Error("isContinuation(nil) = true, want false")
	}
	if isContinuation([]types.Message{}) {
		t.Error("isContinuation([]) = true, want false")
	}
}

func TestIsContinuation_noUserMessage(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "tool", Content: "result", ToolCallID: "c1"},
	}
	if isContinuation(msgs) {
		t.Error("isContinuation with no user message = true, want false")
	}
}

func TestIsContinuation_userThenAssistant(t *testing.T) {
	msgs := []types.Message{
		types.UserMsg("hello"),
		{Role: "assistant", Content: "hi"},
	}
	if isContinuation(msgs) {
		t.Error("isContinuation(user, assistant) = true, want false")
	}
}

func TestIsContinuation_userThenTool(t *testing.T) {
	msgs := []types.Message{
		types.UserMsg("hello"),
		{Role: "tool", Content: "result", ToolCallID: "c1"},
	}
	if !isContinuation(msgs) {
		t.Error("isContinuation(user, tool) = false, want true")
	}
}

func TestIsContinuation_userToolUser(t *testing.T) {
	msgs := []types.Message{
		types.UserMsg("first"),
		{Role: "tool", Content: "result", ToolCallID: "c1"},
		types.UserMsg("second"),
	}
	if isContinuation(msgs) {
		t.Error("isContinuation(user, tool, user) = true, want false (last user has no tools after)")
	}
}

func TestIsContinuation_userToolUserTool(t *testing.T) {
	msgs := []types.Message{
		types.UserMsg("first"),
		{Role: "tool", Content: "result1", ToolCallID: "c1"},
		types.UserMsg("second"),
		{Role: "tool", Content: "result2", ToolCallID: "c2"},
	}
	if !isContinuation(msgs) {
		t.Error("isContinuation(user, tool, user, tool) = false, want true")
	}
}

func TestIsContinuation_multipleUsers(t *testing.T) {
	msgs := []types.Message{
		types.UserMsg("first"),
		{Role: "assistant", Content: "response"},
		types.UserMsg("second"),
		{Role: "tool", Content: "result", ToolCallID: "c1"},
	}
	if !isContinuation(msgs) {
		t.Error("isContinuation with tool after last user = false, want true")
	}
}

func TestIsContinuation_toolBeforeUser(t *testing.T) {
	msgs := []types.Message{
		{Role: "tool", Content: "result", ToolCallID: "c1"},
		types.UserMsg("hello"),
	}
	if isContinuation(msgs) {
		t.Error("isContinuation(tool, user) = true, want false (no tool after last user)")
	}
}

func TestCompactContext_continuationGuard(t *testing.T) {
	fp := &fakeProvider{
		responses: []*types.ChatResponse{{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "summary"},
				FinishReason: "stop",
			}},
		}},
	}
	loop := &Loop{
		Provider:        fp,
		CompactProvider: fp,
		CompactModel:    "test",
		ContextWindow:   100,
		EstimateFactor:  1.3,
	}

	msgs := []types.Message{
		types.SystemMsg("sys"),
	}
	for i := 0; i < 30; i++ {
		msgs = append(msgs, types.UserMsg("msg "+strings.Repeat("x", 50)))
	}
	msgs = append(msgs, types.AssistantMsg("", []types.ToolCall{{
		ID:   "c1",
		Type: "function",
		Function: types.ToolCallFn{
			Name:      "read",
			Arguments: `{"path":"/tmp"}`,
		},
	}}))
	msgs = append(msgs, types.Message{Role: "tool", ToolCallID: "c1", Name: "read", Content: "result"})
	loop.Messages = msgs

	before := len(loop.Messages)
	loop.compactContext(context.Background(), 0.25)

	if len(loop.Messages) != before {
		t.Errorf("compactContext should skip during continuation: before=%d, after=%d", before, len(loop.Messages))
	}
}

func TestCompactContext_preflightEstimate(t *testing.T) {
	fp := &fakeProvider{
		responses: []*types.ChatResponse{{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "summary"},
				FinishReason: "stop",
			}},
		}},
	}
	loop := &Loop{
		Provider:        fp,
		CompactProvider: fp,
		CompactModel:    "test",
		ContextWindow:   100,
		EstimateFactor:  1.3,
	}

	msgs := []types.Message{
		types.SystemMsg("sys"),
	}
	for i := 0; i < 30; i++ {
		msgs = append(msgs, types.UserMsg("msg "+strings.Repeat("x", 50)))
	}
	msgs = append(msgs, types.AssistantMsg("final response", nil))
	loop.Messages = msgs

	if loop.LastPromptTokens != 0 {
		t.Fatalf("expected LastPromptTokens=0 for preflight test")
	}

	before := len(loop.Messages)
	loop.compactContext(context.Background(), 0.25)

	if len(loop.Messages) >= before {
		t.Errorf("compactContext should have compacted: before=%d, after=%d", before, len(loop.Messages))
	}
}

func TestCompactContext_actualTokensPreferred(t *testing.T) {
	fp := &fakeProvider{
		responses: []*types.ChatResponse{{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "summary"},
				FinishReason: "stop",
			}},
		}},
	}
	loop := &Loop{
		Provider:         fp,
		CompactProvider:  fp,
		CompactModel:     "test",
		ContextWindow:    100000,
		EstimateFactor:   1.3,
		LastPromptTokens: 500,
	}

	msgs := []types.Message{
		types.SystemMsg("sys"),
	}
	for i := 0; i < 30; i++ {
		msgs = append(msgs, types.UserMsg("msg "+strings.Repeat("x", 50)))
	}
	msgs = append(msgs, types.AssistantMsg("final", nil))
	loop.Messages = msgs

	before := len(loop.Messages)
	loop.compactContext(context.Background(), 0.25)

	if len(loop.Messages) != before {
		t.Errorf("compactContext should NOT compact when LastPromptTokens (500) < target: before=%d, after=%d", before, len(loop.Messages))
	}
}

func TestCompactContext_customEstimateFactor(t *testing.T) {
	fp := &fakeProvider{
		responses: []*types.ChatResponse{{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "summary"},
				FinishReason: "stop",
			}},
		}},
	}

	msgs := []types.Message{
		types.SystemMsg("sys"),
	}
	for i := 0; i < 20; i++ {
		msgs = append(msgs, types.UserMsg("msg "+strings.Repeat("x", 50)))
	}
	msgs = append(msgs, types.AssistantMsg("final", nil))

	factor1x := &Loop{
		Provider:        fp,
		CompactProvider: fp,
		CompactModel:    "test",
		ContextWindow:   500,
		EstimateFactor:  1.0,
		Messages:        append([]types.Message{}, msgs...),
	}
	factor2x := &Loop{
		Provider:        fp,
		CompactProvider: fp,
		CompactModel:    "test",
		ContextWindow:   500,
		EstimateFactor:  2.0,
		Messages:        append([]types.Message{}, msgs...),
	}

	est1x := preflightTokens(factor1x.Messages, nil, factor1x.EstimateFactor)
	est2x := preflightTokens(factor2x.Messages, nil, factor2x.EstimateFactor)

	if est2x <= est1x {
		t.Errorf("2x factor estimate (%d) should be > 1x factor estimate (%d)", est2x, est1x)
	}
}
