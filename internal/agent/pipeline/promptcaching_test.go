package pipeline

import (
	"context"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func countBreakpoints(t *testing.T, messages []types.Message) int {
	t.Helper()
	n := 0
	for _, m := range messages {
		if m.CacheControl != nil {
			n++
		}
	}
	return n
}

func TestBreakpointCap_Under4(t *testing.T) {
	m := &PromptCachingMiddleware{enabled: true}
	messages := []types.Message{
		types.SystemMsg("sys"),
		types.UserMsg("hi"),
		{Role: "assistant", Content: "hello", ToolCalls: []types.ToolCall{{ID: "1", Type: "function"}}},
		types.ToolResultMsg("1", "echo", "ok"),
		types.UserMsg("again"),
	}
	step := &Step{Messages: messages}
	result, err := m.PrepareStep(context.Background(), step)
	if err != nil {
		t.Fatalf("PrepareStep: %v", err)
	}
	n := countBreakpoints(t, result.Messages)
	if n > anthropicBreakpointCap {
		t.Errorf("expected at most %d breakpoints, got %d", anthropicBreakpointCap, n)
	}
}

func TestBreakpointCap_Over4(t *testing.T) {
	m := &PromptCachingMiddleware{enabled: true}
	messages := []types.Message{
		types.SystemMsg("sys"),
		types.UserMsg("hi"),
		{Role: "assistant", ToolCalls: []types.ToolCall{{ID: "1", Type: "function"}}},
		types.ToolResultMsg("1", "echo", "ok"),
		types.UserMsg("again"),
		{Role: "assistant", ToolCalls: []types.ToolCall{{ID: "2", Type: "function"}}},
		types.ToolResultMsg("2", "echo", "ok"),
		types.UserMsg("yet again"),
		{Role: "assistant", ToolCalls: []types.ToolCall{{ID: "3", Type: "function"}}},
		types.ToolResultMsg("3", "echo", "ok"),
		types.UserMsg("more"),
		{Role: "assistant", ToolCalls: []types.ToolCall{{ID: "4", Type: "function"}}},
		types.ToolResultMsg("4", "echo", "ok"),
		types.UserMsg("final"),
	}
	step := &Step{Messages: messages}
	result, err := m.PrepareStep(context.Background(), step)
	if err != nil {
		t.Fatalf("PrepareStep: %v", err)
	}
	n := countBreakpoints(t, result.Messages)
	if n > anthropicBreakpointCap {
		t.Errorf("expected at most %d breakpoints, got %d", anthropicBreakpointCap, n)
	}
}

func TestBreakpointCap_ZeroMessages(t *testing.T) {
	m := &PromptCachingMiddleware{enabled: true}
	step := &Step{Messages: nil}
	result, err := m.PrepareStep(context.Background(), step)
	if err != nil {
		t.Fatalf("PrepareStep: %v", err)
	}
	n := countBreakpoints(t, result.Messages)
	if n != 0 {
		t.Errorf("expected 0 breakpoints for empty messages, got %d", n)
	}
}

func TestBreakpointCap_SkippedWhenAlreadySet(t *testing.T) {
	m := &PromptCachingMiddleware{enabled: true}
	messages := []types.Message{
		types.SystemMsg("sys"),
		{Role: "tool", Name: "echo", ToolCallID: "1", Content: "ok", CacheControl: &types.CacheControl{Type: "ephemeral"}},
	}
	step := &Step{Messages: messages}
	result, err := m.PrepareStep(context.Background(), step)
	if err != nil {
		t.Fatalf("PrepareStep: %v", err)
	}
	n := countBreakpoints(t, result.Messages)
	if n != 2 {
		t.Errorf("expected 2 breakpoints (system + already-set tool), got %d", n)
	}
}

func TestBreakpointCap_Disabled(t *testing.T) {
	m := &PromptCachingMiddleware{enabled: false}
	messages := []types.Message{
		types.SystemMsg("sys"),
		types.UserMsg("hi"),
	}
	step := &Step{Messages: messages}
	result, err := m.PrepareStep(context.Background(), step)
	if err != nil {
		t.Fatalf("PrepareStep: %v", err)
	}
	n := countBreakpoints(t, result.Messages)
	if n != 0 {
		t.Errorf("expected 0 breakpoints when disabled, got %d", n)
	}
}
