package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestInlineLimitMiddleware_TruncatesAndSynthesizes(t *testing.T) {
	m := NewInlineLimitMiddleware(1)
	msg := &types.Message{ToolCalls: []types.ToolCall{
		{ID: "1", Function: types.ToolCallFn{Name: "read"}},
		{ID: "2", Function: types.ToolCallFn{Name: "read"}},
	}}
	step := &Step{}
	if _, err := m.PostModel(context.Background(), msg, step); err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("calls = %d, want 1", len(msg.ToolCalls))
	}
	if len(step.SynthesizedResults) != 1 {
		t.Fatalf("synthesized = %d, want 1", len(step.SynthesizedResults))
	}
	if !strings.Contains(step.SynthesizedResults[0].Content, "[dropped: this call exceeded the inline tool limit") {
		t.Errorf("unexpected drop message: %q", step.SynthesizedResults[0].Content)
	}
	if step.SynthesizedResults[0].ToolCallID != "2" || step.SynthesizedResults[0].Role != "tool" {
		t.Errorf("synthesized result = %+v, want tool message for call 2", step.SynthesizedResults[0])
	}
}

func TestInlineLimitMiddleware_ZeroIsUnlimited(t *testing.T) {
	m := NewInlineLimitMiddleware(0)
	msg := &types.Message{ToolCalls: make([]types.ToolCall, 5)}
	for i := range msg.ToolCalls {
		msg.ToolCalls[i].ID = string(rune('a' + i))
	}
	step := &Step{}
	if _, err := m.PostModel(context.Background(), msg, step); err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 5 || len(step.SynthesizedResults) != 0 {
		t.Error("zero limit should not truncate")
	}
}

func TestInlineLimitMiddleware_WithinLimitIsNoop(t *testing.T) {
	m := NewInlineLimitMiddleware(3)
	msg := &types.Message{ToolCalls: make([]types.ToolCall, 3)}
	step := &Step{}
	if _, err := m.PostModel(context.Background(), msg, step); err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 3 || len(step.SynthesizedResults) != 0 {
		t.Error("within-limit batch should pass through untouched")
	}
}
