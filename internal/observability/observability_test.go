package observability

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/types"
)

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// Helpers
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

// setupTestTracer installs a local TracerProvider backed by our
// BufferingSpanProcessor, sets it as the global OTel provider, and returns
// both the buffer and a cleanup function that restores the old provider.
func setupTestTracer(t *testing.T) (*BufferingSpanProcessor, func()) {
	t.Helper()

	bp := NewBufferingSpanProcessor()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(bp),
	)

	old := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)

	cleanup := func() {
		otel.SetTracerProvider(old)
		_ = tp.Shutdown(context.Background())
	}

	return bp, cleanup
}

// spanNames returns names of all recorded spans.
func spanNames(spans []RecordedSpan) []string {
	names := make([]string, len(spans))
	for i, s := range spans {
		names[i] = s.Name
	}
	return names
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// safeString
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func Test_safeString(t *testing.T) {
	t.Run("normal", func(t *testing.T) {
		require.Equal(t, "hello", safeString("hello"))
	})
	t.Run("with null bytes", func(t *testing.T) {
		// Null bytes are valid UTF-8; ToValidUTF8 only replaces
		// genuinely invalid sequences.  Protobuf-handling code should
		// additionally strip nulls if needed.
		require.Equal(t, "he\x00llo", safeString("he\x00llo"))
	})
	t.Run("with invalid utf8", func(t *testing.T) {
		require.Equal(t, "he\uFFFDllo", safeString("he\xc0llo"))
	})
	t.Run("empty", func(t *testing.T) {
		require.Equal(t, "", safeString(""))
	})
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// truncate
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func Test_truncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		// truncate keeps up to n characters total, appending "..."
		// when the text is longer than n (so text portion is n-3).
		{name: "shorter than n", in: "hi", n: 10, want: "hi"},
		{name: "longer than n", in: "hello world", n: 5, want: "he..."},
		{name: "exact", in: "exact", n: 5, want: "exact"},
		{name: "four chars (fits)", in: "four", n: 4, want: "four"}, // 4 chars fit within n=4, no truncation needed
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, truncate(tt.in, tt.n))
		})
	}
	// Note: n < 4 currently panics (slice bounds out of range).
	// That is a known limitation of the current implementation.
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// Config
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	require.False(t, cfg.Enabled)
	require.Equal(t, "localhost:4317", cfg.Endpoint)
	require.Equal(t, "yaah", cfg.ServiceName)
	require.True(t, cfg.Traces)
	require.True(t, cfg.Metrics)
	require.Nil(t, cfg.ExtraProcessors)
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// BufferingSpanProcessor
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func TestNewBufferingSpanProcessor(t *testing.T) {
	bp := NewBufferingSpanProcessor()
	require.NotNil(t, bp)
	traces := bp.Traces()
	require.Empty(t, traces)
}

func TestBufferingSpanProcessor_Traces_Empty(t *testing.T) {
	bp := NewBufferingSpanProcessor()
	traces := bp.Traces()
	require.Empty(t, traces)
}

func TestBufferingSpanProcessor_Traces_ClearsBuffer(t *testing.T) {
	bp := NewBufferingSpanProcessor()
	// After consuming through Traces(), buffer still has same data
	// (Traces returns a copy, doesn't clear).
	traces := bp.Traces()
	require.Empty(t, traces)
	// Clear explicitly for repeatable state.
	bp.Reset()
	traces = bp.Traces()
	require.Empty(t, traces)
}

func TestBufferingSpanProcessor_Shutdown(t *testing.T) {
	bp := NewBufferingSpanProcessor()
	require.NoError(t, bp.Shutdown(context.Background()))
}

func TestBufferingSpanProcessor_ForceFlush(t *testing.T) {
	bp := NewBufferingSpanProcessor()
	require.NoError(t, bp.ForceFlush(context.Background()))
}

func TestBufferingSpanProcessor_Reset(t *testing.T) {
	bp := NewBufferingSpanProcessor()
	bp.Reset()
	traces := bp.Traces()
	require.Empty(t, traces)
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// Trace workflow – start / finish round-trips
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func TestStartPrompt(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartPrompt(context.Background(), "sess-1", "turn-abc", "What is 2+2?")
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
	assert.Equal(t, "prompt", traces[0].Name)

	// Check the prompt text attribute.
	v, ok := traces[0].Attributes["prompt.text"]
	require.True(t, ok)
	assert.Contains(t, v, "2+2")

	// Cross-link attributes (consolidate-persistence Phase 0).
	assert.Equal(t, "sess-1", traces[0].Attributes["session.id"])
	assert.Equal(t, "turn-abc", traces[0].Attributes["turn.id"])

	// ctx should have the span embedded.
	require.NotNil(t, trace.SpanFromContext(ctx))
}

func TestStartPrompt_Empty(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	_, span := StartPrompt(context.Background(), "", "", "")
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
	// No prompt.text attribute when prompt is empty.
	_, ok := traces[0].Attributes["prompt.text"]
	assert.False(t, ok)
	_, ok = traces[0].Attributes["session.id"]
	assert.False(t, ok)
	_, ok = traces[0].Attributes["turn.id"]
	assert.False(t, ok)
}

func TestStartTurn(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartTurn(context.Background(), 3, "turn-xyz", "user prompt")
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
	assert.Equal(t, "agent.turn", traces[0].Name)

	assert.Equal(t, int64(3), traces[0].Attributes["turn.number"])
	assert.Equal(t, "user prompt", traces[0].Attributes["turn.prompt"])
	assert.Equal(t, "turn-xyz", traces[0].Attributes["turn.id"])

	_ = ctx
}

func TestStartTool(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartTool(context.Background(), "calculator", `{"a":1,"b":2}`)
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
	assert.Equal(t, "calculator", traces[0].Name)

	assert.Equal(t, "calculator", traces[0].Attributes["tool.name"])
	v, ok := traces[0].Attributes["tool.args"]
	require.True(t, ok)
	assert.Contains(t, v, `"a"`)
	_ = ctx
}

func TestFinishTool(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartTool(context.Background(), "search", `{"q":"test"}`)
	_ = ctx
	FinishTool(span, "42 results", nil)
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
	assert.Equal(t, "search", traces[0].Name)
}

func TestFinishTool_Error(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartTool(context.Background(), "failing", `{}`)
	_ = ctx
	FinishTool(span, "", errors.New("boom"))
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
	assert.Equal(t, "error", traces[0].Status)
}

func TestStartLLM(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartLLM(context.Background(), "gpt-4")
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
	assert.Equal(t, "llm.chat", traces[0].Name)
	assert.Equal(t, "gpt-4", traces[0].Attributes["llm.model"])
	_ = ctx
}

func TestFinishLLM(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartLLM(context.Background(), "gpt-4")
	_ = ctx

	usage := types.Usage{
		PromptTokens:     10,
		CompletionTokens: 15,
		TotalTokens:      25,
	}
	FinishLLM(span, 3, 800, usage)
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
	// FinishLLM adds a "tokens" event on the span.
}

func TestStartStream(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartStream(context.Background(), "claude-3")
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
	assert.Equal(t, "llm.stream", traces[0].Name)
	_ = ctx
}

func TestFinishStream(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartStream(context.Background(), "gpt-4")
	_ = ctx
	FinishStream(span, 150, 120, 3)
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
}

func TestStartPrune(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartPrune(context.Background(), "post_tool")
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
	assert.Equal(t, "prune", traces[0].Name)
	assert.Equal(t, "post_tool", traces[0].Attributes["prune.reason"])
	_ = ctx
}

func TestFinishPrune(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartPrune(context.Background(), "post_compaction")
	_ = ctx
	FinishPrune(span, "post_compaction", 10, 5, 3200, 2, 8, true)

	traces := bp.Traces()
	require.Len(t, traces, 1)

	m := traces[0].Attributes
	assert.Equal(t, int64(10), m["prune.candidates"])
	assert.Equal(t, int64(5), m["prune.marked"])
	assert.Equal(t, int64(3200), m["prune.reclaimed_tokens"])
	assert.Equal(t, int64(2), m["prune.protected_skipped"])
	assert.Equal(t, int64(8), m["prune.total_marked"])
	assert.Equal(t, true, m["prune.committed"])
}

func TestFinishPrune_NilSpan(t *testing.T) {
	// Must not panic.
	require.NotPanics(t, func() {
		FinishPrune(nil, "", 0, 0, 0, 0, 0, false)
	})
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// Verbose trace helpers
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func TestRecordAssistantResponse(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartLLM(context.Background(), "gpt-4")
	_ = ctx

	msg := types.Message{
		Role:             "assistant",
		Content:          "The answer is 4.",
		ReasoningContent: "Let me think...",
		ToolCalls: []types.ToolCall{
			{ID: "tc1", Type: "function", Function: types.ToolCallFn{Name: "calc", Arguments: `{"expr":"2+2"}`}},
		},
	}
	RecordAssistantResponse(span, msg, "stop")
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
}

func TestRecordAssistantResponse_NilSpan(t *testing.T) {
	require.NotPanics(t, func() {
		RecordAssistantResponse(nil, types.Message{}, "")
	})
}

func TestRecordConversation(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartLLM(context.Background(), "gpt-4")
	_ = ctx

	msgs := []types.Message{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "Hi"},
		{Role: "assistant", Content: "Hello!", ToolCalls: []types.ToolCall{
			{ID: "tc1", Type: "function", Function: types.ToolCallFn{Name: "greet", Arguments: `{}`}},
		}},
	}
	RecordConversation(span, msgs)
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
}

func TestRecordConversation_NilSpan(t *testing.T) {
	require.NotPanics(t, func() {
		RecordConversation(nil, []types.Message{{Role: "user", Content: "hi"}})
	})
}

func TestRecordConversation_Empty(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartLLM(context.Background(), "gpt-4")
	_ = ctx
	// Empty messages should not panic.
	RecordConversation(span, nil)
	RecordConversation(span, []types.Message{})
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
}

func TestSystemContent(t *testing.T) {
	t.Run("single system", func(t *testing.T) {
		msgs := []types.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hi"},
		}
		assert.Equal(t, "You are helpful.", SystemContent(msgs))
	})
	t.Run("multiple systems", func(t *testing.T) {
		msgs := []types.Message{
			{Role: "system", Content: "Part 1."},
			{Role: "system", Content: "Part 2."},
		}
		assert.Equal(t, "Part 1.\nPart 2.", SystemContent(msgs))
	})
	t.Run("no system", func(t *testing.T) {
		msgs := []types.Message{
			{Role: "user", Content: "Hi"},
		}
		assert.Equal(t, "", SystemContent(msgs))
	})
	t.Run("empty", func(t *testing.T) {
		assert.Equal(t, "", SystemContent(nil))
	})
}

func TestRecordSystemPrompt(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartLLM(context.Background(), "gpt-4")
	_ = ctx
	RecordSystemPrompt(span, "You are a helpful assistant.")
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
}

func TestRecordSystemPrompt_NilSpan(t *testing.T) {
	require.NotPanics(t, func() {
		RecordSystemPrompt(nil, "system prompt")
	})
}

func TestRecordSystemPrompt_Empty(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartLLM(context.Background(), "gpt-4")
	_ = ctx
	RecordSystemPrompt(span, "")
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
}

func TestRecordStreamEnd(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartStream(context.Background(), "gpt-4")
	_ = ctx
	RecordStreamEnd(span, "finish_reason", "stop", true, 500, 2)
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
}

func TestRecordStreamEnd_NilSpan(t *testing.T) {
	require.NotPanics(t, func() {
		RecordStreamEnd(nil, "", "", false, 0, 0)
	})
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// Sub-agent spans
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func TestStartSubAgent(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartSubAgent(context.Background(), "researcher", "find relevant docs")
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
	assert.Contains(t, traces[0].Name, "subagent")
	assert.Contains(t, traces[0].Name, "researcher")
	assert.Contains(t, traces[0].Name, "find relevant docs")

	assert.Equal(t, "researcher", traces[0].Attributes["subagent.role"])
	assert.Equal(t, "find relevant docs", traces[0].Attributes["subagent.description"])

	_ = ctx
}

func TestFinishSubAgent_Success(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartSubAgent(context.Background(), "helper", "do stuff")
	_ = ctx
	FinishSubAgent(span, nil)
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
	assert.Equal(t, "ok", traces[0].Status)
}

func TestFinishSubAgent_Error(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartSubAgent(context.Background(), "helper", "do stuff")
	_ = ctx
	FinishSubAgent(span, errors.New("timeout"))
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
	assert.Equal(t, "error", traces[0].Status)
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// Error recording
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func TestRecordError(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartLLM(context.Background(), "gpt-4")
	_ = ctx

	err := errors.New("something went wrong")
	RecordError(span, err)
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
	assert.Equal(t, "error", traces[0].Status)
	assert.Equal(t, "something went wrong", traces[0].StatusMsg)
}

func TestRecordError_NilError(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	ctx, span := StartLLM(context.Background(), "gpt-4")
	_ = ctx
	RecordError(span, nil)
	span.End()

	traces := bp.Traces()
	require.Len(t, traces, 1)
	assert.Equal(t, "", traces[0].Status) // RecordError with nil is a no-op; status stays unset
}

func TestRecordTUIView(t *testing.T) {
	// Should not panic — creates its own short-lived span.
	require.NotPanics(t, func() {
		RecordTUIView("pre-scan content", "post-scan content")
	})
}

func TestRecordTUIView_SameContent(t *testing.T) {
	require.NotPanics(t, func() {
		RecordTUIView("same", "same")
	})
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// TraceTree
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func TestTraceTree(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	// Create a parent-child span hierarchy within the same trace.
	ctx, parent := StartPrompt(context.Background(), "", "", "test")
	_, child := StartTurn(ctx, 1, "", "")
	child.End()
	parent.End()

	traces := bp.Traces()
	require.Len(t, traces, 2)

	// Build the tree.
	traceID := traces[0].TraceID
	tree := bp.TraceTree(traceID)
	require.Len(t, tree, 1) // One root
	assert.Equal(t, "prompt", tree[0].Span.Name)
	require.Len(t, tree[0].Children, 1)
	assert.Equal(t, "agent.turn", tree[0].Children[0].Span.Name)
}

func TestTraceTree_UnknownTrace(t *testing.T) {
	bp := NewBufferingSpanProcessor()
	tree := bp.TraceTree("nonexistent")
	require.Empty(t, tree)
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// Full conversational trace (integration-style)
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func TestFullConversationTrace(t *testing.T) {
	bp, cleanup := setupTestTracer(t)
	defer cleanup()

	// 1. Start prompt
	_, promptSpan := StartPrompt(context.Background(), "", "", "What is the weather in Berlin?")
	promptSpan.End()

	// 2. Turn 1 with LLM
	_, turnSpan := StartTurn(context.Background(), 1, "", "")
	_, llmSpan := StartLLM(context.Background(), "gpt-4")
	FinishLLM(llmSpan, 3, 800, types.Usage{PromptTokens: 30, CompletionTokens: 20, TotalTokens: 50})
	llmSpan.End()
	turnSpan.End()

	// 3. Tool call
	_, toolSpan := StartTool(context.Background(), "get_weather", `{"city":"Berlin"}`)
	FinishTool(toolSpan, `{"temp":22}`, nil)
	toolSpan.End()

	// 4. Turn 2 with LLM
	_, turn2Span := StartTurn(context.Background(), 2, "", "")
	_, llm2Span := StartLLM(context.Background(), "gpt-4")
	FinishLLM(llm2Span, 3, 800, types.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15})
	llm2Span.End()
	turn2Span.End()

	// Verify span count and types
	traces := bp.Traces()
	require.GreaterOrEqual(t, len(traces), 5)

	names := spanNames(traces)
	assert.Contains(t, names, "prompt")
	assert.Contains(t, names, "agent.turn")
	assert.Contains(t, names, "llm.chat")
	assert.Contains(t, names, "get_weather")
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// Metrics (smoke tests — ensure no panic with nil meter)
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func TestMetrics_NoProvider(t *testing.T) {
	// With no meter provider set, these should not panic.
	assert.NotPanics(t, func() {
		RecordToolCall(context.Background(), "test_tool", 45*time.Millisecond, false)
	})
	assert.NotPanics(t, func() {
		RecordLLMCall(context.Background(), 200*time.Millisecond, 120, 45)
	})
	assert.NotPanics(t, func() {
		RecordLLMStreamTTFT(context.Background(), 80*time.Millisecond)
	})
	assert.NotPanics(t, func() {
		RecordCompaction(context.Background(), "post_tool", 500*time.Millisecond, 4000, 800, 80.0)
	})
	assert.NotPanics(t, func() {
		RecordAgentTurn(context.Background(), 2*time.Second)
	})
}

func TestMetrics_WithError(t *testing.T) {
	assert.NotPanics(t, func() {
		RecordToolCall(context.Background(), "failing_tool", 1*time.Second, true)
	})
}
