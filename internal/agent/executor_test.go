package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

func TestDualLoop_summaryInjectedAsToolMessage(t *testing.T) {
	outer := &fakeProvider{responses: []*types.ChatResponse{
		{
			Choices: []types.Choice{{
				Message: types.Message{
					Role:    "assistant",
					Content: "I'll delegate the scratch-dir creation.",
					ToolCalls: []types.ToolCall{{
						ID:   "call_outer_1",
						Type: "function",
						Function: types.ToolCallFn{
							Name:      delegateToolName,
							Arguments: `{"task":"create the .scratch/selftest directory"}`,
						},
					}},
				},
				FinishReason: "tool_calls",
			}},
		},
		{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "Done."},
				FinishReason: "stop",
			}},
		},
	}}

	inner := &fakeProvider{responses: []*types.ChatResponse{
		{
			Choices: []types.Choice{{
				Message: types.Message{
					Role: "assistant",
					ToolCalls: []types.ToolCall{{
						ID:   "call_inner_1",
						Type: "function",
						Function: types.ToolCallFn{
							Name:      "bash",
							Arguments: `{"command":"mkdir -p .scratch/selftest"}`,
						},
					}},
				},
				FinishReason: "tool_calls",
			}},
		},
		{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "bash(mkdir): exit 0"},
				FinishReason: "stop",
			}},
		},
	}}

	bashTool := &fakeTool{name: "bash", result: "OK"}
	reg := tools.NewEmptyRegistry()
	reg.Register(bashTool)

	loop := &Loop{
		Provider:           outer,
		ExecutorProvider:   inner,
		Registry:           reg,
		SystemPrompt:       "You are helpful.",
		MaxIterations:      10,
		MaxInnerIterations: 10,
	}

	resp, err := loop.Run(context.Background(), "self-test the bash tool")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if resp != "Done." {
		t.Errorf("response = %q, want %q", resp, "Done.")
	}

	var summaryToolMsg *types.Message
	var assistantSummaryCount int
	for i := range loop.Messages {
		m := &loop.Messages[i]
		if m.Role == "tool" && m.ToolCallID == "call_outer_1" {
			summaryToolMsg = m
		}
		if m.Role == "assistant" && strings.Contains(m.Content, "bash(mkdir): exit 0") {
			assistantSummaryCount++
		}
	}
	if summaryToolMsg == nil {
		t.Fatalf("expected a tool message for call_outer_1 carrying the executor summary")
	}
	if !strings.Contains(summaryToolMsg.Content, "bash(mkdir): exit 0") {
		t.Errorf("tool message content = %q, want it to carry the executor summary", summaryToolMsg.Content)
	}
	if !strings.Contains(summaryToolMsg.Content, `<executor_result state="completed"`) {
		t.Errorf("tool message missing structured envelope: %q", summaryToolMsg.Content)
	}
	if summaryToolMsg.Name != delegateToolName {
		t.Errorf("tool message name = %q, want %q", summaryToolMsg.Name, delegateToolName)
	}
	if assistantSummaryCount != 0 {
		t.Errorf("executor summary was injected as an assistant message (%d found) — this is the feedback-loop bug", assistantSummaryCount)
	}
}

func TestDualLoop_DelegateRoutesIntentThroughRun(t *testing.T) {
	outer := &fakeProvider{
		responses: []*types.ChatResponse{
			{
				Choices: []types.Choice{{
					Message:      types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{ID: "d1", Type: "function", Function: types.ToolCallFn{Name: delegateToolName, Arguments: `{"task":"list go files in internal/agent"}`}}}},
					FinishReason: "tool_calls",
				}},
			},
			{
				Choices: []types.Choice{{
					Message:      types.Message{Role: "assistant", Content: "Found 3 files."},
					FinishReason: "stop",
				}},
			},
		},
	}
	inner := &fakeProvider{
		responses: []*types.ChatResponse{
			{
				Choices: []types.Choice{{
					Message:      types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{ID: "i1", Type: "function", Function: types.ToolCallFn{Name: "glob", Arguments: `{"pattern":"internal/agent/*.go"}`}}}},
					FinishReason: "tool_calls",
				}},
			},
			{
				Choices: []types.Choice{{
					Message:      types.Message{Role: "assistant", Content: "glob: 3 matches"},
					FinishReason: "stop",
				}},
			},
		},
	}
	reg := tools.NewEmptyRegistry()
	reg.Register(&fakeTool{name: "glob", result: "a.go\nb.go\nc.go"})
	loop := &Loop{Provider: outer, ExecutorProvider: inner, Registry: reg,
		MaxIterations: 5, MaxInnerIterations: 5}

	resp, err := loop.Run(context.Background(), "how many go files are in internal/agent")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp != "Found 3 files." {
		t.Fatalf("resp = %q", resp)
	}

	if len(inner.requests) == 0 {
		t.Fatalf("executor was never invoked")
	}
	var payload string
	for _, m := range inner.requests[0].Messages {
		if m.Role == "user" {
			payload = m.Content
			break
		}
	}
	if !strings.Contains(payload, "how many go files are in internal/agent") {
		t.Fatalf("executor did not receive original intent through Run(): %q", payload)
	}

	var found bool
	for i := range loop.Messages {
		m := &loop.Messages[i]
		if m.Role == "tool" && m.ToolCallID == "d1" && strings.Contains(m.Content, "glob") {
			found = true
		}
	}
	if !found {
		t.Fatalf("delegate tool result not injected for call d1")
	}
}

func TestRun_InlineCallDoesNotSpawnExecutor(t *testing.T) {
	outer := &fakeProvider{responses: []*types.ChatResponse{
		{Choices: []types.Choice{{Message: types.Message{Role: "assistant",
			ToolCalls: []types.ToolCall{{ID: "c1", Type: "function",
				Function: types.ToolCallFn{Name: "read", Arguments: `{"filePath":"f"}`}}}},
			FinishReason: "tool_calls"}}},
		{Choices: []types.Choice{{Message: types.Message{Role: "assistant", Content: "done"}, FinishReason: "stop"}}},
	}}
	reg := tools.NewEmptyRegistry()
	reg.Register(&fakeTool{name: "read", result: "data"})
	execP := &fakeProvider{responses: []*types.ChatResponse{{}}}
	loop := &Loop{Provider: outer, ExecutorProvider: execP, Registry: reg, MaxIterations: 5}

	if _, err := loop.Run(context.Background(), "read f"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(execP.requests) != 0 {
		t.Fatalf("inline call must not spawn executor; executor got %d requests", len(execP.requests))
	}
}

func toolDefNames(defs []types.ToolDef) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Function.Name)
	}
	return out
}

func TestPlannerToolSet_AlwaysHasDelegate(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	reg.Register(&fakeTool{name: "bash", result: "ok"})
	reg.Register(&fakeTool{name: "read", result: "ok"})

	loop := &Loop{Registry: reg}
	got := loop.buildPlannerToolDefs()
	names := toolDefNames(got)
	if !contains(names, "delegate") {
		t.Fatalf("delegate must always be present; got %v", names)
	}
	if !contains(names, "bash") || !contains(names, "read") {
		t.Fatalf("full tool set must be retained alongside delegate; got %v", names)
	}
}

func TestResolveExecutor_DefaultFallback(t *testing.T) {
	mainP := &fakeProvider{}
	loop := &Loop{Provider: mainP, Model: "main-model"}
	p, m := loop.resolveExecutor("")
	if p != mainP || m != "main-model" {
		t.Fatalf("expected default fallback, got provider=%p model=%q", p, m)
	}
}

func TestResolveExecutor_DedicatedWhenConfigured(t *testing.T) {
	execP := &fakeProvider{}
	mainP := &fakeProvider{}
	loop := &Loop{Provider: mainP, ExecutorProvider: execP, ExecutorModel: "cheap", Model: "main"}
	p, m := loop.resolveExecutor("default")
	if p != execP || m != "cheap" {
		t.Fatalf("expected dedicated executor, got provider=%p model=%q", p, m)
	}
}

func TestExecutorReceivesOriginalIntent(t *testing.T) {
	inner := &fakeProvider{responses: []*types.ChatResponse{
		{Choices: []types.Choice{{
			Message:      types.Message{Role: "assistant", Content: "read(f): 10B"},
			FinishReason: "stop",
		}}},
	}}
	reg := tools.NewEmptyRegistry()
	reg.Register(&fakeTool{name: "read", result: "10B"})

	loop := &Loop{
		Provider:           &fakeProvider{},
		ExecutorProvider:   inner,
		Registry:           reg,
		SystemPrompt:       "PLANNER-IDENTITY",
		Model:              "m",
		MaxInnerIterations: 5,
	}

	summary, exhausted, err := loop.runExecutor(
		context.Background(),
		"read the file and report its size",
		"tell me about f",
		"default",
	)
	if err != nil {
		t.Fatalf("runExecutor: %v", err)
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
		t.Fatalf("executor reused the planner identity prompt — must use executorSystemPrompt")
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

func TestExecutorChainsTools(t *testing.T) {
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

	loop := &Loop{
		Provider:           &fakeProvider{},
		ExecutorProvider:   inner,
		Registry:           reg,
		MaxInnerIterations: 5,
	}
	summary, exhausted, err := loop.runExecutor(context.Background(), "find and read go files", "", "default")
	if err != nil {
		t.Fatalf("runExecutor: %v", err)
	}
	if exhausted {
		t.Fatalf("should not be exhausted")
	}
	if !strings.Contains(summary, "glob") || !strings.Contains(summary, "read") {
		t.Fatalf("summary should name both chained tools: %q", summary)
	}
}

func TestExecutor_DefaultModelFallbackThroughRun(t *testing.T) {
	mainP := &fakeProvider{responses: []*types.ChatResponse{
		{Choices: []types.Choice{{Message: types.Message{Role: "assistant", Content: "read(f): 8B"}, FinishReason: "stop"}}},
	}}
	reg := tools.NewEmptyRegistry()
	reg.Register(&fakeTool{name: "read", result: "8B"})
	loop := &Loop{Provider: mainP, Registry: reg, Model: "main", MaxInnerIterations: 5}

	summary, _, err := loop.runExecutor(context.Background(), "report file size", "how big is f", "default")
	if err != nil {
		t.Fatalf("runExecutor with no dedicated executor: %v", err)
	}
	if summary != "read(f): 8B" {
		t.Fatalf("summary = %q", summary)
	}
	if len(mainP.requests) != 1 {
		t.Fatalf("default-model executor should have used the main provider once, got %d requests", len(mainP.requests))
	}
}
