package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

type fakeProvider struct {
	responses []*types.ChatResponse
	index     int
	requests  []types.ChatRequest
	failErr   error
	maxFails  int
	failCount int
}

func (f *fakeProvider) Send(_ context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	f.requests = append(f.requests, req)
	if f.failCount < f.maxFails {
		f.failCount++
		return nil, f.failErr
	}
	if f.index >= len(f.responses) {
		return &types.ChatResponse{Choices: []types.Choice{{Message: types.Message{Role: "assistant", Content: "done"}, FinishReason: "stop"}}, Usage: types.Usage{TotalTokens: 10}}, nil
	}
	resp := f.responses[f.index]
	f.index++
	return resp, nil
}

type fakeTool struct {
	name   string
	result string
}

func (f *fakeTool) Name() string        { return f.name }
func (f *fakeTool) Description() string { return "fake " + f.name }
func (f *fakeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (f *fakeTool) Execute(_ context.Context, _ string) (string, error) {
	return f.result, nil
}

func TestReceivesOriginalIntent(t *testing.T) {
	inner := &fakeProvider{responses: []*types.ChatResponse{
		{Choices: []types.Choice{{
			Message:      types.Message{Role: "assistant", Content: "read(f): 10B"},
			FinishReason: "stop",
		}}},
	}}
	reg := tools.NewEmptyRegistry()
	reg.Register(&fakeTool{name: "read", result: "10B"})

	exec := &Executor{
		Provider:      inner,
		Registry:      reg,
		MaxIterations: 5,
		OuterModel:    "m",
		OnUsage:       func(types.Usage) {},
	}

	summary, exhausted, _, err := exec.Run(
		context.Background(),
		"read the file and report its size",
		"tell me about f",
		"default",
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exhausted {
		t.Fatalf("should not be exhausted for a one-shot read")
	}
	if summary != "read(f): 10B" {
		t.Fatalf("summary = %q, want %q", summary, "read(f): 10B")
	}

	if len(inner.requests) != 1 {
		t.Fatalf("expected exactly 1 executor request, got %d", len(inner.requests))
	}
	msgs := inner.requests[0].Messages

	if len(msgs) == 0 || msgs[0].Role != "system" {
		t.Fatalf("executor saw no leading system message: %+v", msgs)
	}
	if msgs[0].Content == "PLANNER-IDENTITY" {
		t.Fatalf("executor reused the planner identity prompt — must use executor identity prompt")
	}
	if !strings.Contains(msgs[0].Content, "tool executor") {
		t.Fatalf("executor system message does not look like the executor prompt: %q", msgs[0].Content)
	}

	var payload string
	for _, m := range msgs {
		if m.Role == "user" {
			payload = m.Content
			break
		}
	}
	if !strings.Contains(payload, "tell me about f") {
		t.Fatalf("executor payload missing original intent: %q", payload)
	}
	if !strings.Contains(payload, "read the file and report its size") {
		t.Fatalf("executor payload missing the directive: %q", payload)
	}
}

func TestChainsTools(t *testing.T) {
	inner := &fakeProvider{responses: []*types.ChatResponse{
		{Choices: []types.Choice{{
			Message: types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{
				ID: "i1", Type: "function",
				Function: types.ToolCallFn{Name: "glob", Arguments: `{"pattern":"*.go"}`},
			}}},
			FinishReason: "tool_calls",
		}}},
		{Choices: []types.Choice{{
			Message: types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{
				ID: "i2", Type: "function",
				Function: types.ToolCallFn{Name: "read", Arguments: `{"filePath":"a.go"}`},
			}}},
			FinishReason: "tool_calls",
		}}},
		{Choices: []types.Choice{{
			Message:      types.Message{Role: "assistant", Content: "glob: 1 match; read(a.go): 50B"},
			FinishReason: "stop",
		}}},
	}}
	reg := tools.NewEmptyRegistry()
	reg.Register(&fakeTool{name: "glob", result: "a.go"})
	reg.Register(&fakeTool{name: "read", result: "50B"})

	exec := &Executor{
		Provider:      inner,
		Registry:      reg,
		MaxIterations: 5,
		OnUsage:       func(types.Usage) {},
	}
	summary, exhausted, _, err := exec.Run(context.Background(), "find and read go files", "", "default")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exhausted {
		t.Fatalf("should not be exhausted")
	}
	if !strings.Contains(summary, "glob") || !strings.Contains(summary, "read") {
		t.Fatalf("summary should name both chained tools: %q", summary)
	}
}

func TestDefaultModelFallback(t *testing.T) {
	mainP := &fakeProvider{responses: []*types.ChatResponse{
		{Choices: []types.Choice{{Message: types.Message{Role: "assistant", Content: "read(f): 8B"}, FinishReason: "stop"}}},
	}}
	reg := tools.NewEmptyRegistry()
	reg.Register(&fakeTool{name: "read", result: "8B"})
	exec := &Executor{
		Provider:      mainP,
		Registry:      reg,
		MaxIterations: 5,
		OnUsage:       func(types.Usage) {},
	}

	summary, _, _, err := exec.Run(context.Background(), "report file size", "how big is f", "default")
	if err != nil {
		t.Fatalf("Run with no dedicated executor: %v", err)
	}
	if summary != "read(f): 8B" {
		t.Fatalf("summary = %q", summary)
	}
	if len(mainP.requests) != 1 {
		t.Fatalf("default-model executor should have used the main provider once, got %d requests", len(mainP.requests))
	}
}

func TestFallbackToMainProviderOnError(t *testing.T) {
	execP := &fakeProvider{failErr: fmt.Errorf("executor model 429: rate limited"), maxFails: 1}
	mainP := &fakeProvider{responses: []*types.ChatResponse{
		{Choices: []types.Choice{{Message: types.Message{Role: "assistant", Content: "read(f): done"}, FinishReason: "stop"}}},
	}}
	reg := tools.NewEmptyRegistry()
	reg.Register(&fakeTool{name: "read", result: "ok"})
	exec := &Executor{
		Provider:         execP,
		FallbackProvider: mainP,
		Model:            "exec-model",
		Registry:         reg,
		MaxIterations:    5,
		OuterModel:       "main-model",
		OnUsage:          func(types.Usage) {},
	}

	summary, exhausted, fellBack, err := exec.Run(context.Background(), "read f", "", "default")
	if err != nil {
		t.Fatalf("Run with fallback: %v", err)
	}
	if exhausted {
		t.Fatalf("should not be exhausted after fallback")
	}
	if !fellBack {
		t.Fatalf("expected fellBack=true when executor provider fails")
	}
	if summary != "read(f): done" {
		t.Fatalf("summary = %q, want %q", summary, "read(f): done")
	}
	if execP.failCount != 1 {
		t.Fatalf("executor provider should have been tried exactly once, got %d failures", execP.failCount)
	}
	if len(mainP.requests) != 1 {
		t.Fatalf("main provider should have been tried exactly once after fallback, got %d", len(mainP.requests))
	}
}

func TestNoFallbackWhenBothFail(t *testing.T) {
	execP := &fakeProvider{failErr: fmt.Errorf("executor 429"), maxFails: 1}
	mainP := &fakeProvider{failErr: fmt.Errorf("main 503"), maxFails: 1}
	reg := tools.NewEmptyRegistry()
	reg.Register(&fakeTool{name: "read", result: "ok"})
	exec := &Executor{
		Provider:         execP,
		FallbackProvider: mainP,
		Model:            "exec-model",
		Registry:         reg,
		MaxIterations:    5,
		OuterModel:       "main-model",
		OnUsage:          func(types.Usage) {},
	}

	_, _, fellBack, err := exec.Run(context.Background(), "read f", "", "default")
	if err == nil {
		t.Fatalf("expected error when both providers fail")
	}
	if !fellBack {
		t.Fatalf("expected fellBack=true before propagating the error")
	}
}

func TestNoFallbackWhenNoDedicatedProvider(t *testing.T) {
	mainP := &fakeProvider{failErr: fmt.Errorf("main 503"), maxFails: 1}
	reg := tools.NewEmptyRegistry()
	exec := &Executor{
		Provider:      mainP,
		Model:         "main-model",
		Registry:      reg,
		MaxIterations: 5,
		OnUsage:       func(types.Usage) {},
	}

	_, _, fellBack, err := exec.Run(context.Background(), "read f", "", "default")
	if err == nil {
		t.Fatalf("expected error when main provider fails with no executor")
	}
	if fellBack {
		t.Fatalf("expected fellBack=false with no dedicated executor provider")
	}
}
