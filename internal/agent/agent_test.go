package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/agent/errorclassify"
	"github.com/buchenberg/yaah/internal/memory"
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
	loop := &Loop{DisableInnerLoop: true,
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
	loop := &Loop{DisableInnerLoop: true,
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
	loop := &Loop{DisableInnerLoop: true,
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
	loop := &Loop{DisableInnerLoop: true,
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
	loop := &Loop{DisableInnerLoop: true,
		Provider:         fp,
		Registry:         reg,
		SystemPrompt:     "test",
		MaxIterations:    10,
		LoopDetectCount:  3,
		LoopDetectWindow: 5,
	}

	_, err := loop.Run(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for loop detection")
	}
	if !strings.Contains(err.Error(), "loop detected") {
		t.Errorf("expected loop detection error, got: %v", err)
	}
}

func TestLoop_noFalsePositiveOnDifferentArgs(t *testing.T) {
	// Test that loop detection does NOT trigger when the same tool returns
	// identical results for different arguments (e.g. writing different files).
	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			{
				Choices: []types.Choice{{
					Message: types.Message{
						Role: "assistant",
						ToolCalls: []types.ToolCall{{
							ID:       "call_1",
							Type:     "function",
							Function: types.ToolCallFn{Name: "write", Arguments: `{"path":"/tmp/a.go","content":"aaa"}`},
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
							ID:       "call_2",
							Type:     "function",
							Function: types.ToolCallFn{Name: "write", Arguments: `{"path":"/tmp/b.go","content":"bbb"}`},
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
							ID:       "call_3",
							Type:     "function",
							Function: types.ToolCallFn{Name: "write", Arguments: `{"path":"/tmp/c.go","content":"ccc"}`},
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
							ID:       "call_4",
							Type:     "function",
							Function: types.ToolCallFn{Name: "write", Arguments: `{"path":"/tmp/d.go","content":"ddd"}`},
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
							ID:       "call_5",
							Type:     "function",
							Function: types.ToolCallFn{Name: "write", Arguments: `{"path":"/tmp/e.go","content":"eee"}`},
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

	// All writes return the same success message — but with different args.
	reg := tools.NewRegistry()
	reg.Register(&fakeTool{name: "write", result: "File written successfully"})
	loop := &Loop{DisableInnerLoop: true,
		Provider:         fp,
		Registry:         reg,
		SystemPrompt:     "test",
		MaxIterations:    10,
		LoopDetectCount:  3,
		LoopDetectWindow: 5,
	}

	resp, err := loop.Run(context.Background(), "write five files")
	if err != nil {
		t.Fatalf("loop detection false positive: different args should not trigger loop: %v", err)
	}
	if resp != "done" {
		t.Errorf("expected 'done', got %q", resp)
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
	loop := &Loop{DisableInnerLoop: true,
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
	loop := &Loop{DisableInnerLoop: true,
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
	loop := &Loop{DisableInnerLoop: true,
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
	loop := &Loop{DisableInnerLoop: true,
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
	loop := &Loop{DisableInnerLoop: true,
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
	// LLM compaction keeps sysMsg + summary + token-budget recent messages + assistant response
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
	loop := &Loop{DisableInnerLoop: true,
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

// --- Test: Session persistence ---

func TestLoop_persistMessageNilDB(t *testing.T) {
	loop := &Loop{DisableInnerLoop: true, DB: nil, SessionID: "test"}
	loop.persistMessage(types.Message{Role: "user", Content: "hello"})
	// Should not panic
}

func TestLoop_persistMessageToDB(t *testing.T) {
	tmp := t.TempDir()
	db, err := memory.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	db.CreateSession(memory.Session{
		ID: "sess-1", StartedAt: time.Now().Unix(), CWD: "/tmp", Model: "test",
	})

	loop := &Loop{DisableInnerLoop: true, DB: db, SessionID: "sess-1"}
	loop.persistMessage(types.Message{Role: "system", Content: "you are a bot"})
	loop.persistMessage(types.Message{Role: "user", Content: "hello"})

	msgs, err := db.GetMessages("sess-1")
	if err != nil {
		t.Fatalf("GetMessages() error: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[0].Content != "you are a bot" {
		t.Errorf("msg[0] = role=%s content=%s", msgs[0].Role, msgs[0].Content)
	}
	if msgs[1].Role != "user" || msgs[1].Content != "hello" {
		t.Errorf("msg[1] = role=%s content=%s", msgs[1].Role, msgs[1].Content)
	}
	if msgs[0].Idx != 0 {
		t.Errorf("msg[0].Idx = %d, want 0", msgs[0].Idx)
	}
	if msgs[1].Idx != 1 {
		t.Errorf("msg[1].Idx = %d, want 1", msgs[1].Idx)
	}
}

func TestLoop_persistMessageWithToolCall(t *testing.T) {
	tmp := t.TempDir()
	db, err := memory.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	db.CreateSession(memory.Session{
		ID: "sess-1", StartedAt: time.Now().Unix(), CWD: "/tmp", Model: "test",
	})

	loop := &Loop{DisableInnerLoop: true, DB: db, SessionID: "sess-1"}
	assistantMsg := types.Message{
		Role: "assistant",
		ToolCalls: []types.ToolCall{{
			ID:   "call_abc",
			Type: "function",
			Function: types.ToolCallFn{
				Name:      "read",
				Arguments: `{"filePath":"/tmp/foo"}`,
			},
		}},
	}
	loop.persistMessage(assistantMsg)

	toolMsg := types.ToolResultMsg("call_abc", "read", "file contents here")
	loop.persistMessage(toolMsg)

	msgs, err := db.GetMessages("sess-1")
	if err != nil {
		t.Fatalf("GetMessages() error: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	// Assistant message should have serialized tool calls
	if msgs[0].Role != "assistant" {
		t.Errorf("msg[0].Role = %s, want assistant", msgs[0].Role)
	}
	if msgs[0].ToolCalls == "" {
		t.Error("msg[0].ToolCalls should not be empty")
	}
	if !strings.Contains(msgs[0].ToolCalls, "call_abc") {
		t.Errorf("ToolCalls should contain call_abc: %s", msgs[0].ToolCalls)
	}

	// Tool message should have ToolCallID and ToolName
	if msgs[1].Role != "tool" {
		t.Errorf("msg[1].Role = %s, want tool", msgs[1].Role)
	}
	if msgs[1].ToolCallID != "call_abc" {
		t.Errorf("msg[1].ToolCallID = %s, want call_abc", msgs[1].ToolCallID)
	}
	if msgs[1].ToolName != "read" {
		t.Errorf("msg[1].ToolName = %s, want read", msgs[1].ToolName)
	}
	if msgs[1].Content != "file contents here" {
		t.Errorf("msg[1].Content = %s, want file contents here", msgs[1].Content)
	}
}

func TestLoop_sessionPersistenceAcrossRunCalls(t *testing.T) {
	tmp := t.TempDir()
	db, err := memory.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	sessionID := "sess-persist"
	db.CreateSession(memory.Session{
		ID: sessionID, StartedAt: time.Now().Unix(), CWD: "/tmp", Model: "test",
	})

	fp := &fakeProvider{
		responses: []*types.ChatResponse{{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "Hello from first run"},
				FinishReason: "stop",
			}},
		}},
	}

	reg := tools.NewRegistry()
	loop := &Loop{DisableInnerLoop: true,
		Provider:      fp,
		Registry:      reg,
		SystemPrompt:  "You are a test bot.",
		SessionID:     sessionID,
		DB:            db,
		MaxIterations: 5,
	}

	resp, err := loop.Run(context.Background(), "first message")
	if err != nil {
		t.Fatalf("first Run() error: %v", err)
	}
	if resp != "Hello from first run" {
		t.Errorf("first response = %q", resp)
	}

	msgs, err := db.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("GetMessages() error: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (system + user + assistant), got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "system" {
		t.Errorf("msg[0].Role = %s, want system", msgs[0].Role)
	}
	if msgs[1].Role != "user" || msgs[1].Content != "first message" {
		t.Errorf("msg[1] = role=%s content=%s", msgs[1].Role, msgs[1].Content)
	}
	if msgs[2].Role != "assistant" || msgs[2].Content != "Hello from first run" {
		t.Errorf("msg[2] = role=%s content=%s", msgs[2].Role, msgs[2].Content)
	}
}

func TestLoop_sessionPersistenceWithToolCalls(t *testing.T) {
	tmp := t.TempDir()
	db, err := memory.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	sessionID := "sess-tools"
	db.CreateSession(memory.Session{
		ID: sessionID, StartedAt: time.Now().Unix(), CWD: "/tmp", Model: "test",
	})

	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			{
				Choices: []types.Choice{{
					Message: types.Message{
						Role: "assistant",
						ToolCalls: []types.ToolCall{{
							ID:   "call_read",
							Type: "function",
							Function: types.ToolCallFn{
								Name:      "read",
								Arguments: `{"filePath":"/tmp/test.txt"}`,
							},
						}},
					},
					FinishReason: "tool_calls",
				}},
			},
			{
				Choices: []types.Choice{{
					Message:      types.Message{Role: "assistant", Content: "File read successfully"},
					FinishReason: "stop",
				}},
			},
		},
	}

	reg := tools.NewRegistry()
	loop := &Loop{DisableInnerLoop: true,
		Provider:      fp,
		Registry:      reg,
		SystemPrompt:  "You are a test bot.",
		SessionID:     sessionID,
		DB:            db,
		MaxIterations: 5,
	}

	resp, err := loop.Run(context.Background(), "read /tmp/test.txt")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if resp != "File read successfully" {
		t.Errorf("response = %q", resp)
	}

	msgs, err := db.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("GetMessages() error: %v", err)
	}
	// Expected: system, user, assistant(tool_calls), tool(result), assistant(final)
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(msgs))
	}

	// Check message sequence
	roles := []string{"system", "user", "assistant", "tool", "assistant"}
	for i, want := range roles {
		if msgs[i].Role != want {
			t.Errorf("msg[%d].Role = %s, want %s", i, msgs[i].Role, want)
		}
	}

	// Tool message should have tool_call_id
	if msgs[3].ToolCallID != "call_read" {
		t.Errorf("tool msg ToolCallID = %s, want call_read", msgs[3].ToolCallID)
	}
	if msgs[3].ToolName != "read" {
		t.Errorf("tool msg ToolName = %s, want read", msgs[3].ToolName)
	}

	// Assistant message with tool calls should have serialized ToolCalls
	if msgs[2].ToolCalls == "" {
		t.Error("assistant msg with tool calls has empty ToolCalls")
	}
	if !strings.Contains(msgs[2].ToolCalls, "call_read") {
		t.Errorf("ToolCalls missing call_read: %s", msgs[2].ToolCalls)
	}
}

func TestLoop_sessionPersistenceMultipleTurns(t *testing.T) {
	tmp := t.TempDir()
	db, err := memory.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	sessionID := "sess-multi"
	db.CreateSession(memory.Session{
		ID: sessionID, StartedAt: time.Now().Unix(), CWD: "/tmp", Model: "test",
	})

	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			{Choices: []types.Choice{{Message: types.Message{Role: "assistant", Content: "Turn 1 response"}, FinishReason: "stop"}}},
			{Choices: []types.Choice{{Message: types.Message{Role: "assistant", Content: "Turn 2 response"}, FinishReason: "stop"}}},
		},
	}

	reg := tools.NewRegistry()
	loop := &Loop{DisableInnerLoop: true,
		Provider:      fp,
		Registry:      reg,
		SystemPrompt:  "You are a test bot.",
		SessionID:     sessionID,
		DB:            db,
		MaxIterations: 5,
	}

	// Run turn 1
	resp1, err := loop.Run(context.Background(), "turn 1")
	if err != nil {
		t.Fatalf("Run() turn 1 error: %v", err)
	}
	if resp1 != "Turn 1 response" {
		t.Errorf("response 1 = %q", resp1)
	}

	// Run turn 2 (Messages persists across runs)
	resp2, err := loop.Run(context.Background(), "turn 2")
	if err != nil {
		t.Fatalf("Run() turn 2 error: %v", err)
	}
	if resp2 != "Turn 2 response" {
		t.Errorf("response 2 = %q", resp2)
	}

	msgs, err := db.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("GetMessages() error: %v", err)
	}
	// Expected: system, user1, assistant1, user2, assistant2 = 5 messages
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(msgs))
	}
	expectedRoles := []string{"system", "user", "assistant", "user", "assistant"}
	for i, want := range expectedRoles {
		if msgs[i].Role != want {
			t.Errorf("msg[%d].Role = %s, want %s", i, msgs[i].Role, want)
		}
	}
	if msgs[4].Content != "Turn 2 response" {
		t.Errorf("last msg content = %q", msgs[4].Content)
	}
}

func TestLoop_persistMessageNoDB(t *testing.T) {
	loop := &Loop{DisableInnerLoop: true, DB: nil, SessionID: "test"}
	loop.MsgIdx = 5
	loop.persistMessage(types.Message{Role: "user", Content: "hello"})
	// Should be a no-op, not increment MsgIdx
	if loop.MsgIdx != 5 {
		t.Errorf("MsgIdx should not change when DB is nil, got %d", loop.MsgIdx)
	}
}

func TestLoop_persistMessageEmptyContent(t *testing.T) {
	tmp := t.TempDir()
	db, err := memory.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	db.CreateSession(memory.Session{
		ID: "sess-1", StartedAt: time.Now().Unix(), CWD: "/tmp", Model: "test",
	})

	loop := &Loop{DisableInnerLoop: true, DB: db, SessionID: "sess-1"}
	assistantMsg := types.Message{
		Role:    "assistant",
		Content: "",
		ToolCalls: []types.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: types.ToolCallFn{
				Name:      "bash",
				Arguments: `{"command":"ls"}`,
			},
		}},
	}
	loop.persistMessage(assistantMsg)

	msgs, err := db.GetMessages("sess-1")
	if err != nil {
		t.Fatalf("GetMessages() error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	// Empty content with tool calls should have a synthetic content
	if msgs[0].Content == "" {
		t.Error("empty content with tool calls should produce synthetic content")
	}
	if !strings.Contains(msgs[0].Content, "[tool:bash]") {
		t.Errorf("synthetic content missing tool info: %q", msgs[0].Content)
	}
}

// --- Test: Context-overflow auto-compact ---

func TestIsContextOverflowError(t *testing.T) {
	positives := []string{
		"context length exceeded",
		"too many tokens in the request",
		"reduce the length of your prompt",
		"maximum context window reached",
		"max tokens limit exceeded",
		"token limit reached",
		"prompt is too long for this model",
		"context window exceeded for model gpt-4o",
		"requested token count exceeds the maximum",
	}
	for _, msg := range positives {
		name := msg
		if len(name) > 30 {
			name = name[:27] + "..."
		}
		t.Run(name, func(t *testing.T) {
			if !errorclassify.IsContextOverflow(fmtErrorf(msg), errorclassify.ErrorMeta{}) {
				t.Errorf("expected true for: %q", msg)
			}
		})
	}

	negatives := []string{
		"rate limit exceeded",
		"authentication failed",
		"connection refused",
		"timeout",
		"",
	}
	for _, msg := range negatives {
		t.Run(msg, func(t *testing.T) {
			if errorclassify.IsContextOverflow(fmtErrorf(msg), errorclassify.ErrorMeta{}) {
				t.Errorf("expected false for: %q", msg)
			}
		})
	}
}

func TestLoop_autoCompactOnContextOverflow(t *testing.T) {
	fp := &fakeProvider{
		maxFails: 1,
		failErr:  fmtErrorf("context length exceeded: maximum tokens 4096"),
		responses: []*types.ChatResponse{{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "success after compact"},
				FinishReason: "stop",
			}},
		}},
	}

	reg := tools.NewRegistry()
	loop := &Loop{DisableInnerLoop: true,
		Provider:        fp,
		Registry:        reg,
		SystemPrompt:    "test prompt",
		MaxIterations:   3,
		MaxRetries:      0, // No regular retries — auto-compact handles it
		ContextWindow:   1000,
		CompactProvider: fp,
		CompactModel:    "test",
	}

	// Pre-populate with enough messages to need compaction
	loop.Messages = []types.Message{
		types.SystemMsg("test prompt"),
	}
	for i := 0; i < 20; i++ {
		loop.Messages = append(loop.Messages, types.UserMsg("message "+strings.Repeat("x", 80)))
		loop.Messages = append(loop.Messages, types.AssistantMsg("response "+strings.Repeat("y", 80), nil))
	}

	resp, err := loop.Run(context.Background(), "test")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if resp != "success after compact" {
		t.Errorf("response = %q", resp)
	}
	// Two LLM calls: first failed (context overflow), then compaction succeeded, then original call succeeded
	if fp.failCount != 1 {
		t.Errorf("expected 1 failure before success, got %d", fp.failCount)
	}
}

func TestLoop_autoCompactDoesNotTriggerOnNonContextError(t *testing.T) {
	var compactionCalls int
	reg := tools.NewRegistry()

	// Use a provider that returns a non-context error
	fp := &fakeProvider{
		maxFails: 3,
		failErr:  fmtErrorf("authentication failed"),
	}

	loop := &Loop{DisableInnerLoop: true,
		Provider:      fp,
		Registry:      reg,
		SystemPrompt:  "test",
		MaxIterations: 3,
		MaxRetries:    2,
		ContextWindow: 1000,
	}

	// Pre-populate so we can check if compaction was called
	loop.Messages = []types.Message{
		types.SystemMsg("test"),
	}
	for i := 0; i < 15; i++ {
		loop.Messages = append(loop.Messages, types.UserMsg("msg"))
	}
	_ = compactionCalls

	_, err := loop.Run(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error on non-context failure")
	}
	if !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("expected auth error, got: %v", err)
	}
}

func TestLoop_autoCompactCapped(t *testing.T) {
	// Verify that auto-compact doesn't retry forever.
	// compactContext only compacts if len(rest) > 4, so after one compact
	// the second call should fall through to normal backoff.
	compactProvider := &fakeProvider{
		failCount: 0,
		maxFails:  0,
	}
	_ = compactProvider

	fp := &fakeProvider{
		maxFails: 10,
		failErr:  fmtErrorf("token limit exceeded"),
	}

	reg := tools.NewRegistry()
	loop := &Loop{DisableInnerLoop: true,
		Provider:            fp,
		Registry:            reg,
		SystemPrompt:        "test",
		MaxIterations:       3,
		MaxRetries:          2,
		ContextWindow:       500,
		CompactProvider:     fp,
		CompactModel:        "test",
		CompactionThreshold: 0.5,
	}

	// Enough messages for one compact to help
	loop.Messages = []types.Message{
		types.SystemMsg("test"),
	}
	for i := 0; i < 30; i++ {
		loop.Messages = append(loop.Messages, types.UserMsg("message "+strings.Repeat("x", 50)))
	}

	_, err := loop.Run(context.Background(), "test")
	if err == nil {
		t.Fatal("expected eventual error")
	}
	// Should have attempted multiple compacts + normal retries
	if !strings.Contains(err.Error(), "token limit exceeded") {
		t.Errorf("expected token limit error, got: %v", err)
	}
}

func TestConflictDetection_ReportsConflictInConversation(t *testing.T) {
	tracker := &tools.ConflictTracker{}
	tracker.Record("worker — Add login handler", "/tmp/test.go", "write")
	tracker.Record("worker — Refactor auth", "/tmp/test.go", "edit")

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
								Name:      "read",
								Arguments: `{"path":"/tmp/test.go"}`,
							},
						}},
					},
					FinishReason: "tool_calls",
				}},
				Usage: types.Usage{TotalTokens: 10},
			},
			{
				Choices: []types.Choice{{
					Message:      types.Message{Role: "assistant", Content: "Resolved."},
					FinishReason: "stop",
				}},
				Usage: types.Usage{TotalTokens: 10},
			},
		},
	}

	reg := tools.NewEmptyRegistry()
	reg.Register(&fakeTool{name: "read", result: "file contents"})

	loop := &Loop{DisableInnerLoop: true,
		Provider:        fp,
		Registry:        reg,
		SystemPrompt:    "test",
		MaxIterations:   10,
		ConflictTracker: tracker,
	}

	_, err := loop.Run(context.Background(), "check for conflicts")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, msg := range loop.Messages {
		if strings.Contains(msg.Content, "CONFLICT:") {
			found = true
			if !strings.Contains(msg.Content, "worker — Add login handler") {
				t.Errorf("expected first worker label in conflict message")
			}
			if !strings.Contains(msg.Content, "worker — Refactor auth") {
				t.Errorf("expected second worker label in conflict message")
			}
			if !strings.Contains(msg.Content, "/tmp/test.go") {
				t.Errorf("expected file path in conflict message")
			}
			break
		}
	}
	if !found {
		t.Error("conflict message was not injected into conversation")
	}
}

func TestConflictDetection_NoConflictWhenTrackerEmpty(t *testing.T) {
	tracker := &tools.ConflictTracker{}

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
								Name:      "read",
								Arguments: `{"path":"/tmp/test.go"}`,
							},
						}},
					},
					FinishReason: "tool_calls",
				}},
				Usage: types.Usage{TotalTokens: 10},
			},
			{
				Choices: []types.Choice{{
					Message:      types.Message{Role: "assistant", Content: "done"},
					FinishReason: "stop",
				}},
				Usage: types.Usage{TotalTokens: 10},
			},
		},
	}

	reg := tools.NewEmptyRegistry()
	reg.Register(&fakeTool{name: "read", result: "file contents"})

	loop := &Loop{DisableInnerLoop: true,
		Provider:        fp,
		Registry:        reg,
		SystemPrompt:    "test",
		MaxIterations:   10,
		ConflictTracker: tracker,
	}

	_, err := loop.Run(context.Background(), "no conflicts here")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, msg := range loop.Messages {
		if strings.Contains(msg.Content, "CONFLICT:") {
			t.Error("unexpected conflict message when tracker is empty")
			break
		}
	}
}

func TestConflictDetection_TrackerClearedAfterIteration(t *testing.T) {
	tracker := &tools.ConflictTracker{}
	tracker.Record("w1", "/a.go", "write")
	tracker.Record("w2", "/a.go", "edit")

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
								Name:      "read",
								Arguments: `{"path":"/a.go"}`,
							},
						}},
					},
					FinishReason: "tool_calls",
				}},
				Usage: types.Usage{TotalTokens: 10},
			},
			{
				Choices: []types.Choice{{
					Message:      types.Message{Role: "assistant", Content: "first"},
					FinishReason: "stop",
				}},
				Usage: types.Usage{TotalTokens: 10},
			},
			{
				Choices: []types.Choice{{
					Message: types.Message{
						Role: "assistant",
						ToolCalls: []types.ToolCall{{
							ID:   "call_2",
							Type: "function",
							Function: types.ToolCallFn{
								Name:      "read",
								Arguments: `{"path":"/a.go"}`,
							},
						}},
					},
					FinishReason: "tool_calls",
				}},
				Usage: types.Usage{TotalTokens: 10},
			},
			{
				Choices: []types.Choice{{
					Message:      types.Message{Role: "assistant", Content: "second"},
					FinishReason: "stop",
				}},
				Usage: types.Usage{TotalTokens: 10},
			},
		},
	}

	reg := tools.NewEmptyRegistry()
	reg.Register(&fakeTool{name: "read", result: "file contents"})

	loop := &Loop{DisableInnerLoop: true,
		Provider:        fp,
		Registry:        reg,
		SystemPrompt:    "test",
		MaxIterations:   10,
		ConflictTracker: tracker,
	}

	loop.Run(context.Background(), "first prompt")

	conflictCount := 0
	for _, msg := range loop.Messages {
		if strings.Contains(msg.Content, "CONFLICT:") {
			conflictCount++
		}
	}
	if conflictCount != 1 {
		t.Errorf("expected exactly 1 conflict message in first run, got %d", conflictCount)
	}
}

type fmtErrorf string

func (e fmtErrorf) Error() string { return string(e) }

// TestFormatToolCallsForExecutor_noInstructionsRefeed locks in fix 1: the
// outer model's msg.Content (its reasoning/narrative prose) must NOT be
// re-fed to the inner executor as "## Instructions". That re-feed caused
// the inner model to re-interpret prose like "run a comprehensive
// self-test of all tools" as its task scope and exhaust its iteration
// budget. The task prompt should list only the required tool calls.
func TestFormatToolCallsForExecutor_noInstructionsRefeed(t *testing.T) {
	loop := &Loop{SystemPrompt: "sys"}
	msg := types.Message{
		Role:    "assistant",
		Content: "Let me run a comprehensive self-test of all file-based tools.",
		ToolCalls: []types.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: types.ToolCallFn{
				Name:      "bash",
				Arguments: `{"command":"mkdir -p .scratch/selftest"}`,
			},
		}},
	}
	task := loop.formatToolCallsForExecutor(msg)

	if strings.Contains(task, "## Instructions") {
		t.Errorf("task prompt re-feeds outer model prose as ## Instructions: %q", task)
	}
	if strings.Contains(task, "comprehensive self-test") {
		t.Errorf("task prompt leaks outer model prose into inner task: %q", task)
	}
	if !strings.Contains(task, "## Required tool calls") {
		t.Errorf("task prompt missing required tool calls section: %q", task)
	}
	if !strings.Contains(task, "bash") {
		t.Errorf("task prompt missing the bash tool call: %q", task)
	}
}

// TestDualLoop_summaryInjectedAsToolMessage locks in fix 2: the inner
// executor's summary is carried by the final tool-result message, not by
// a separate assistant message. Injecting it as an assistant message made
// the outer model respond to its own inner summary ("Done. All tools
// executed...") with a new narrative beat, producing a multi-turn
// feedback loop for what should be one turn.
func TestDualLoop_summaryInjectedAsToolMessage(t *testing.T) {
	// Outer provider: turn 0 emits a bash tool call + prose; turn 1
	// produces the final answer after seeing the inner executor's result.
	outer := &fakeProvider{responses: []*types.ChatResponse{
		{
			Choices: []types.Choice{{
				Message: types.Message{
					Role:    "assistant",
					Content: "Let me create the scratch dir.",
					ToolCalls: []types.ToolCall{{
						ID:   "call_outer_1",
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
				Message:      types.Message{Role: "assistant", Content: "Done."},
				FinishReason: "stop",
			}},
		},
	}}

	// Inner executor: iteration 0 executes the bash call; iteration 1
	// returns a terse structured summary.
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
	reg := tools.NewRegistry()
	reg.Register(bashTool)

	loop := &Loop{
		Provider:           outer,
		ExecutorProvider:   inner,
		Registry:           reg,
		SystemPrompt:       "You are helpful.",
		MaxIterations:      10,
		MaxInnerIterations: 10,
		// DisableInnerLoop defaults to false → dual-loop path runs.
	}

	resp, err := loop.Run(context.Background(), "self-test the bash tool")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if resp != "Done." {
		t.Errorf("response = %q, want %q", resp, "Done.")
	}

	// The summary must live in a tool message (the final one for the
	// outer tool_call_id), not in a standalone assistant message.
	var summaryToolMsg *types.Message
	var assistantSummaryCount int
	for i := range loop.Messages {
		m := &loop.Messages[i]
		if m.Role == "tool" && m.ToolCallID == "call_outer_1" {
			if summaryToolMsg == nil || len(m.Content) > len(summaryToolMsg.Content) {
				summaryToolMsg = m
			}
		}
		if m.Role == "assistant" && strings.Contains(m.Content, "bash(mkdir): exit 0") {
			assistantSummaryCount++
		}
	}
	if summaryToolMsg == nil {
		t.Fatalf("expected a tool message for call_outer_1 carrying the summary")
	}
	if !strings.Contains(summaryToolMsg.Content, "bash(mkdir): exit 0") {
		t.Errorf("tool message content = %q, want it to carry the inner summary", summaryToolMsg.Content)
	}
	if assistantSummaryCount != 0 {
		t.Errorf("inner summary was injected as an assistant message (%d found) — this is the feedback-loop bug", assistantSummaryCount)
	}
}

// --- executor-owns-tools architecture (Task 1) ------------------------------

// toolDefNames returns the function names of a ToolDef slice, in order.
func toolDefNames(defs []types.ToolDef) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Function.Name)
	}
	return out
}

// sameSet reports whether two string slices hold the same elements ignoring
// order (and ignoring duplicates).
func sameSet(a, b []string) bool {
	am := map[string]bool{}
	for _, s := range a {
		am[s] = true
	}
	for _, s := range b {
		if !am[s] {
			return false
		}
		delete(am, s)
	}
	return len(am) == 0
}

// TestDualLoopInactiveByDefault locks in the opt-in contract: with no
// ExecutorProvider configured, the dual-loop must NOT engage. This is the
// waste fix — the default path (and every subagent, which never sets an
// ExecutorProvider) runs the single-loop path.
func TestDualLoopInactiveByDefault(t *testing.T) {
	loop := &Loop{Provider: &fakeProvider{}, Registry: tools.NewRegistry()}
	if loop.dualLoopActive() {
		t.Fatalf("dual-loop must be inactive when ExecutorProvider is nil")
	}
}

// TestDualLoopActiveWhenExecutorConfigured: an explicitly configured
// executor is the sole trigger for the dual-loop.
func TestDualLoopActiveWhenExecutorConfigured(t *testing.T) {
	loop := &Loop{
		Provider:         &fakeProvider{},
		ExecutorProvider: &fakeProvider{},
		Registry:         tools.NewRegistry(),
	}
	if !loop.dualLoopActive() {
		t.Fatalf("dual-loop must be active when ExecutorProvider is set")
	}
}

// TestPlannerToolSet_DelegateOnlyWhenActive enforces the "one decision, one
// owner" boundary by schema: when the dual-loop is active the planner may
// only call `delegate`; it cannot see file/bash tools. When inactive, it
// gets the full set (single-loop path).
func TestPlannerToolSet_DelegateOnlyWhenActive(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	reg.Register(&fakeTool{name: "bash", result: "ok"})
	reg.Register(&fakeTool{name: "read", result: "ok"})

	// Inactive → planner sees the full tool set.
	inactive := &Loop{Registry: reg}
	if got := inactive.buildPlannerToolDefs(); !sameSet(toolDefNames(got), []string{"bash", "read"}) {
		t.Fatalf("inactive planner tools = %v, want [bash read]", toolDefNames(got))
	}

	// Active → planner sees only delegate.
	active := &Loop{Registry: reg, ExecutorProvider: &fakeProvider{}}
	got := active.buildPlannerToolDefs()
	if names := toolDefNames(got); len(names) != 1 || names[0] != "delegate" {
		t.Fatalf("active planner tools = %v, want [delegate]", names)
	}
}
