package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// fakeProvider implements Provider for testing.
type fakeProvider struct {
	responses []*types.ChatResponse
	index     int
	requests  []types.ChatRequest
	// retry testing
	failCount int
	maxFails  int
	failErr   error
}

func (f *fakeProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	if f.failCount < f.maxFails {
		f.failCount++
		return nil, f.failErr
	}
	f.requests = append(f.requests, req)
	if f.index >= len(f.responses) {
		return &types.ChatResponse{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "done"},
				FinishReason: "stop",
			}},
			Usage: types.Usage{TotalTokens: 10},
		}, nil
	}
	resp := f.responses[f.index]
	f.index++
	return resp, nil
}

// fakeStreamProvider implements StreamProvider for testing.
type fakeStreamProvider struct {
	chunks  []providers.StreamChunk
	closeCh chan struct{}
	// retry testing
	failCount int
	maxFails  int
	failErr   error
}

func (f *fakeStreamProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	return &types.ChatResponse{Choices: []types.Choice{{Message: types.Message{Content: "ok"}}}}, nil
}

func (f *fakeStreamProvider) SendStream(ctx context.Context, req types.ChatRequest) (<-chan providers.StreamChunk, <-chan error) {
	chunks := make(chan providers.StreamChunk)
	errs := make(chan error, 1)

	go func() {
		defer close(chunks)
		defer close(errs)

		if f.failCount < f.maxFails {
			f.failCount++
			errs <- f.failErr
			return
		}

		for _, c := range f.chunks {
			select {
			case chunks <- c:
			case <-ctx.Done():
				return
			case <-f.closeCh:
				return
			}
		}
	}()
	return chunks, errs
}

// fakeTool is a configurable tool for testing.
type fakeTool struct {
	name    string
	result  string
	err     error
	delay   time.Duration
	callCnt *int
	mu      *sync.Mutex
}

func (t *fakeTool) Name() string        { return t.name }
func (t *fakeTool) Description() string { return "fake test tool" }
func (t *fakeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func (t *fakeTool) Execute(ctx context.Context, args string) (string, error) {
	if t.delay > 0 {
		select {
		case <-time.After(t.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if t.mu != nil {
		t.mu.Lock()
		if t.callCnt != nil {
			*t.callCnt++
		}
		t.mu.Unlock()
	}
	if t.err != nil {
		return "", t.err
	}
	return t.result, nil
}

func TestLoop_plainTextResponse(t *testing.T) {
	fp := &fakeProvider{
		responses: []*types.ChatResponse{{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "Hello! How can I help?"},
				FinishReason: "stop",
			}},
		}},
	}

	reg := tools.NewRegistry()
	loop := &Loop{
		Provider:      fp,
		Registry:      reg,
		SystemPrompt:  "You are helpful.",
		MaxIterations: 10,
	}

	resp, err := loop.Run(context.Background(), "Hi")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if resp != "Hello! How can I help?" {
		t.Errorf("response = %q", resp)
	}
	if len(fp.requests) != 1 {
		t.Errorf("expected 1 request, got %d", len(fp.requests))
	}
}

func TestLoop_toolCalling(t *testing.T) {
	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			{
				// First response: model wants to call a tool
				Choices: []types.Choice{{
					Message: types.Message{
						Role: "assistant",
						ToolCalls: []types.ToolCall{{
							ID:   "call_1",
							Type: "function",
							Function: types.ToolCallFn{
								Name:      "read",
								Arguments: `{"path":"/tmp/nonexistent"}`,
							},
						}},
					},
					FinishReason: "tool_calls",
				}},
			},
			{
				// Second response: model summarizes tool output
				Choices: []types.Choice{{
					Message:      types.Message{Role: "assistant", Content: "The file doesn't exist."},
					FinishReason: "stop",
				}},
			},
		},
	}

	reg := tools.NewRegistry()
	loop := &Loop{
		Provider:      fp,
		Registry:      reg,
		SystemPrompt:  "You are helpful.",
		MaxIterations: 10,
	}

	resp, err := loop.Run(context.Background(), "Read /tmp/nonexistent")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if resp != "The file doesn't exist." {
		t.Errorf("response = %q", resp)
	}
	// Should have made 2 requests (tool call + follow-up)
	if len(fp.requests) != 2 {
		t.Errorf("expected 2 requests, got %d", len(fp.requests))
	}
}

func TestLoop_hitsMaxIterations(t *testing.T) {
	// Model keeps calling tools forever
	fp := &fakeProvider{}
	for i := 0; i < 20; i++ {
		fp.responses = append(fp.responses, &types.ChatResponse{
			Choices: []types.Choice{{
				Message: types.Message{
					Role: "assistant",
					ToolCalls: []types.ToolCall{{
						ID:   "call_loop",
						Type: "function",
						Function: types.ToolCallFn{
							Name:      "read",
							Arguments: `{"path":"/dev/null"}`,
						},
					}},
				},
				FinishReason: "tool_calls",
			}},
		})
	}

	reg := tools.NewRegistry()
	loop := &Loop{
		Provider:      fp,
		Registry:      reg,
		SystemPrompt:  "You are helpful.",
		MaxIterations: 3,
	}

	_, err := loop.Run(context.Background(), "loop forever")
	if err == nil {
		t.Fatal("expected error for max iterations")
	}
	if len(fp.requests) > 3 {
		t.Errorf("expected at most 3 requests, got %d", len(fp.requests))
	}
}

// --- Test: Tool result truncation ---

func TestLoop_toolResultTruncation(t *testing.T) {
	longText := strings.Repeat("x", 10000)

	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			{
				Choices: []types.Choice{{
					Message: types.Message{
						Role: "assistant",
						ToolCalls: []types.ToolCall{{
							ID:   "call_1",
							Type: "function",
							Function: types.ToolCallFn{
								Name:      "echo",
								Arguments: `{}`,
							},
						}},
					},
					FinishReason: "tool_calls",
				}},
			},
			{
				Choices: []types.Choice{{
					Message:      types.Message{Role: "assistant", Content: "got it"},
					FinishReason: "stop",
				}},
			},
		},
	}

	reg := tools.NewRegistry()
	// Add a tool that returns a long result
	reg.Register(&fakeTool{name: "echo", result: longText})
	loop := &Loop{
		Provider:      fp,
		Registry:      reg,
		SystemPrompt:  "test",
		MaxIterations: 5,
	}

	_, err := loop.Run(context.Background(), "test")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(fp.requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(fp.requests))
	}

	// The tool result sent back to the model should be truncated
	lastUserMsg := fp.requests[1].Messages
	var toolResult string
	for _, m := range lastUserMsg {
		if m.Role == "tool" {
			toolResult = m.Content
			break
		}
	}

	if len(toolResult) > 9000 {
		t.Errorf("tool result not truncated: len=%d, content=%q...", len(toolResult), toolResult[:80])
	}
	if !strings.Contains(toolResult, "[truncated]") {
		t.Errorf("truncation marker missing from tool result: %q", toolResult)
	}
}

// --- Test: Parallel tool execution ---

func TestLoop_loopDetection(t *testing.T) {
	// Test that loop detection works in middleware mode.
	// If the model keeps calling the same tool with the same result,
	// it should eventually halt.
	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			{
				Choices: []types.Choice{{
					Message: types.Message{
						Role: "assistant",
						ToolCalls: []types.ToolCall{{
							ID:   "call_1",
							Type: "function",
							Function: types.ToolCallFn{
								Name:      "echo",
								Arguments: `{}`,
							},
						}},
					},
					FinishReason: "tool_calls",
				}},
			},
			{
				Choices: []types.Choice{{
					Message: types.Message{
						Role: "assistant",
						ToolCalls: []types.ToolCall{{
							ID:   "call_2",
							Type: "function",
							Function: types.ToolCallFn{
								Name:      "echo",
								Arguments: `{}`,
							},
						}},
					},
					FinishReason: "tool_calls",
				}},
			},
			{
				Choices: []types.Choice{{
					Message: types.Message{
						Role: "assistant",
						ToolCalls: []types.ToolCall{{
							ID:   "call_3",
							Type: "function",
							Function: types.ToolCallFn{
								Name:      "echo",
								Arguments: `{}`,
							},
						}},
					},
					FinishReason: "tool_calls",
				}},
			},
			{
				Choices: []types.Choice{{
					Message: types.Message{
						Role: "assistant",
						ToolCalls: []types.ToolCall{{
							ID:   "call_4",
							Type: "function",
							Function: types.ToolCallFn{
								Name:      "echo",
								Arguments: `{}`,
							},
						}},
					},
					FinishReason: "tool_calls",
				}},
			},
			{
				Choices: []types.Choice{{
					Message: types.Message{
						Role: "assistant",
						ToolCalls: []types.ToolCall{{
							ID:   "call_5",
							Type: "function",
							Function: types.ToolCallFn{
								Name:      "echo",
								Arguments: `{}`,
							},
						}},
					},
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

	reg := tools.NewRegistry()
	reg.Register(&fakeTool{name: "echo", result: "same result"})
	loop := &Loop{
		Provider:         fp,
		Registry:         reg,
		SystemPrompt:     "test",
		MaxIterations:    10,
		LoopDetectCount:  3,
		LoopDetectWindow: 5,
		AgentMode:        "middleware",
	}

	_, err := loop.Run(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for loop detection")
	}
	if !strings.Contains(err.Error(), "loop detected") {
		t.Errorf("expected loop detection error, got: %v", err)
	}
}

func TestLoop_parallelToolExecution(t *testing.T) {
	var mu sync.Mutex
	var cnt int

	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			{
				Choices: []types.Choice{{
					Message: types.Message{
						Role: "assistant",
						ToolCalls: []types.ToolCall{
							{
								ID: "call_1", Type: "function",
								Function: types.ToolCallFn{Name: "slow1", Arguments: `{}`},
							},
							{
								ID: "call_2", Type: "function",
								Function: types.ToolCallFn{Name: "slow2", Arguments: `{}`},
							},
						},
					},
					FinishReason: "tool_calls",
				}},
			},
			{
				Choices: []types.Choice{{
					Message:      types.Message{Role: "assistant", Content: "both done"},
					FinishReason: "stop",
				}},
			},
		},
	}

	reg := tools.NewRegistry()
	reg.Register(&fakeTool{name: "slow1", result: "result1", delay: 100 * time.Millisecond, callCnt: &cnt, mu: &mu})
	reg.Register(&fakeTool{name: "slow2", result: "result2", delay: 100 * time.Millisecond, callCnt: &cnt, mu: &mu})
	loop := &Loop{
		Provider:      fp,
		Registry:      reg,
		SystemPrompt:  "test",
		MaxIterations: 5,
	}

	start := time.Now()
	_, err := loop.Run(context.Background(), "test")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Both tools should have been called
	if cnt != 2 {
		t.Errorf("expected 2 tool calls, got %d", cnt)
	}

	// Parallel execution should be faster than sequential (2 * 100ms = 200ms)
	// Allow some overhead but must be under 180ms
	if elapsed > 180*time.Millisecond {
		t.Errorf("tools executed sequentially? elapsed=%v, expected < 180ms (parallel)", elapsed)
	}

	// Verify tool results are in correct order (call_1 result, then call_2 result)
	msgs := fp.requests[1].Messages
	var toolResults []string
	for _, m := range msgs {
		if m.Role == "tool" {
			toolResults = append(toolResults, m.Content)
		}
	}
	if len(toolResults) != 2 {
		t.Fatalf("expected 2 tool results, got %d", len(toolResults))
	}
	if toolResults[0] != "result1" || toolResults[1] != "result2" {
		t.Errorf("tool result order wrong: %v", toolResults)
	}
}

// --- Test: Retry with backoff ---

func TestLoop_retryOnError(t *testing.T) {
	fp := &fakeProvider{
		maxFails: 2,
		failErr:  errors.New("temporary failure"),
		responses: []*types.ChatResponse{{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "success after retries"},
				FinishReason: "stop",
			}},
		}},
	}

	reg := tools.NewRegistry()
	loop := &Loop{
		Provider:      fp,
		Registry:      reg,
		SystemPrompt:  "test",
		MaxIterations: 5,
		MaxRetries:    3,
	}

	resp, err := loop.Run(context.Background(), "test")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if resp != "success after retries" {
		t.Errorf("response = %q", resp)
	}
	if fp.failCount != 2 {
		t.Errorf("expected 2 failures before success, got %d", fp.failCount)
	}
}

func TestLoop_retryExceedsMax(t *testing.T) {
	fp := &fakeProvider{
		maxFails: 5,
		failErr:  errors.New("persistent failure"),
	}

	reg := tools.NewRegistry()
	loop := &Loop{
		Provider:      fp,
		Registry:      reg,
		SystemPrompt:  "test",
		MaxIterations: 5,
		MaxRetries:    2,
	}

	_, err := loop.Run(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error when retries exhausted")
	}
	if !strings.Contains(err.Error(), "persistent failure") {
		t.Errorf("error = %v", err)
	}
}

// --- Test: Token usage tracking ---

func TestLoop_tokenUsageTracking(t *testing.T) {
	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			{
				Choices: []types.Choice{{
					Message:      types.Message{Role: "assistant", Content: "Hello"},
					FinishReason: "stop",
				}},
				Usage: types.Usage{PromptTokens: 50, CompletionTokens: 30, TotalTokens: 80},
			},
		},
	}

	reg := tools.NewRegistry()
	loop := &Loop{
		Provider:      fp,
		Registry:      reg,
		SystemPrompt:  "test",
		MaxIterations: 5,
	}

	_, err := loop.Run(context.Background(), "test")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if loop.TotalTokens.PromptTokens != 50 {
		t.Errorf("PromptTokens = %d, want 50", loop.TotalTokens.PromptTokens)
	}
	if loop.TotalTokens.CompletionTokens != 30 {
		t.Errorf("CompletionTokens = %d, want 30", loop.TotalTokens.CompletionTokens)
	}
	if loop.TotalTokens.TotalTokens != 80 {
		t.Errorf("TotalTokens = %d, want 80", loop.TotalTokens.TotalTokens)
	}
}

// --- Test: Context window management ---

func TestLoop_contextWindowTrimming(t *testing.T) {
	reg := tools.NewRegistry()
	loop := &Loop{
		Provider:      &fakeProvider{},
		Registry:      reg,
		SystemPrompt:  "test",
		MaxIterations: 1,
		ContextWindow: 500,
	}

	loop.Messages = []types.Message{
		types.SystemMsg("test prompt that is fairly long and takes up some token space"),
	}
	for i := 0; i < 20; i++ {
		loop.Messages = append(loop.Messages, types.UserMsg("message number "+strings.Repeat("x", 100)))
		loop.Messages = append(loop.Messages, types.AssistantMsg("response number "+strings.Repeat("y", 100), nil))
	}

	_, err := loop.Run(context.Background(), "new message")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	totalChars := 0
	for _, m := range loop.Messages {
		totalChars += len(m.Content)
	}
	estimatedTokens := totalChars / 4
	if estimatedTokens > 1000 {
		t.Errorf("messages not trimmed: estimated %d tokens for %d chars in %d messages",
			estimatedTokens, totalChars, len(loop.Messages))
	}
	// LLM compaction keeps sysMsg + summary + 6 recent messages + assistant response
	if len(loop.Messages) < 5 {
		t.Errorf("expected some messages preserved, got %d", len(loop.Messages))
	}
}

// --- Test: Thinking/reasoning wiring ---

func TestLoop_thinkingCallback(t *testing.T) {
	var thinkingText string
	var mu sync.Mutex

	bsp := &fakeStreamProvider{
		chunks: []providers.StreamChunk{
			{Choices: []providers.StreamChoice{{Delta: providers.StreamDelta{ReasoningContent: "Let me think"}}}},
			{Choices: []providers.StreamChoice{{Delta: providers.StreamDelta{ReasoningContent: " about this..."}}}},
			{Choices: []providers.StreamChoice{{Delta: providers.StreamDelta{Content: "The answer is 42."}, FinishReason: strPtr("stop")}}},
		},
	}

	reg := tools.NewRegistry()
	loop := &Loop{
		Provider:      bsp,
		Registry:      reg,
		SystemPrompt:  "test",
		MaxIterations: 5,
		OnToken:       func(token string) {},
		OnThinking: func(text string) {
			mu.Lock()
			thinkingText += text
			mu.Unlock()
		},
	}

	resp, err := loop.Run(context.Background(), "question")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	mu.Lock()
	if thinkingText != "Let me think about this..." {
		t.Errorf("thinking text = %q, want 'Let me think about this...'", thinkingText)
	}
	mu.Unlock()

	if resp != "The answer is 42." {
		t.Errorf("response = %q", resp)
	}
}

func strPtr(s string) *string { return &s }
