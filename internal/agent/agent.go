package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// Provider is the interface for model backends.
type Provider interface {
	Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error)
}

// StreamProvider is a provider that supports streaming responses.
type StreamProvider interface {
	Provider
	SendStream(ctx context.Context, req types.ChatRequest) (<-chan providers.StreamChunk, <-chan error)
}

// TokenCallback is called for each streamed token.
type TokenCallback func(token string)

// ToolInfo contains information about a tool call for display.
type ToolInfo struct {
	Name     string        // tool name
	Args     string        // abbreviated arguments
	Duration time.Duration // how long the tool took
	Result   string        // truncated tool result (only on second call)
	Error    string        // error message if the tool failed
}

// ToolCallback is called before and after each tool execution.
// The first call (before) has Duration=0 and Error="".
// The second call (after) has the actual Duration and any Error.
type ToolCallback func(info ToolInfo)

// ThinkingCallback is called when the model outputs thinking/reasoning text.
type ThinkingCallback func(text string)

// FlushCallback is called when the model finishes a streaming segment and
// is about to start a tool call or a new iteration. The TUI uses this to
// flush the accumulated streaming content into the message list so the
// next segment starts on a fresh line.
type FlushCallback func(content string)

// ToolResultMaxLen is the maximum length of a tool result before truncation.
const ToolResultMaxLen = 8192

// Loop runs the agent conversation loop.
type Loop struct {
	Provider      Provider
	Registry      *tools.Registry
	SystemPrompt  string
	Model         string
	MaxIterations int
	OnToken       TokenCallback
	OnTool        ToolCallback
	OnThinking    ThinkingCallback
	OnFlush       FlushCallback

	// ContextWindow is the estimated token budget for the conversation.
	// When the total estimated tokens exceed 80% of this value, old messages
	// are trimmed (system prompt + recent messages are preserved).
	// Default 0 means no trimming.
	ContextWindow int

	// MaxRetries is the number of retries on transient provider errors.
	// Default 0 means no retries.
	MaxRetries int

	// RetryBackoff is the base backoff duration. Default 1s.
	RetryBackoff time.Duration

	// TotalTokens accumulates token usage across all API calls in the loop.
	TotalTokens types.Usage

	// Messages holds the conversation history across multiple Run calls.
	Messages []types.Message
}

// Run executes the full conversation loop for a single user message.
func (l *Loop) Run(ctx context.Context, userInput string) (string, error) {
	if l.MaxIterations <= 0 {
		l.MaxIterations = 50
	}
	if l.Model == "" {
		l.Model = "gpt-4o-mini"
	}
	if l.RetryBackoff <= 0 {
		l.RetryBackoff = time.Second
	}

	if l.Messages != nil {
		l.Messages = append(l.Messages, types.UserMsg(userInput))
	} else {
		l.Messages = []types.Message{
			types.SystemMsg(l.SystemPrompt),
			types.UserMsg(userInput),
		}
	}

	if l.ContextWindow > 0 {
		l.trimContext()
	}

	messages := l.Messages

	toolDefs := l.buildToolDefs()

	for iter := 0; iter < l.MaxIterations; iter++ {
		select {
		case <-ctx.Done():
			l.Messages = messages
			return "", ctx.Err()
		default:
		}

		req := types.ChatRequest{
			Model:    l.Model,
			Messages: messages,
			Tools:    toolDefs,
		}

		msg, streamed, err := l.getAssistantMessage(ctx, req)
		if err != nil {
			l.Messages = messages
			return "", fmt.Errorf("provider error: %w", err)
		}
		messages = append(messages, msg)

		if len(msg.ToolCalls) == 0 {
			l.Messages = messages
			return msg.Content, nil
		}

		if streamed && msg.Content != "" && l.OnFlush != nil {
			l.OnFlush(msg.Content)
		}

		l.executeToolsParallel(ctx, msg.ToolCalls, &messages)
	}

	l.Messages = messages
	return "", fmt.Errorf("max iterations (%d) reached without final response", l.MaxIterations)
}

// getAssistantMessage returns the next assistant message with retry logic.
func (l *Loop) getAssistantMessage(ctx context.Context, req types.ChatRequest) (types.Message, bool, error) {
	var lastMsg types.Message
	var wasStreamed bool
	var lastErr error

	for attempt := 0; attempt <= l.MaxRetries; attempt++ {
		var msg types.Message
		var streamed bool
		var err error

		if sp, ok := l.Provider.(StreamProvider); ok && l.OnToken != nil {
			msg, err = l.runStream(ctx, sp, req)
			streamed = true
		} else {
			resp, sendErr := l.Provider.Send(ctx, req)
			if sendErr != nil {
				err = sendErr
			} else {
				l.captureUsage(resp)
				if len(resp.Choices) == 0 {
					err = fmt.Errorf("no choices in response")
				} else {
					msg = resp.Choices[0].Message
				}
			}
		}

		if err == nil {
			return msg, streamed, nil
		}

		lastMsg = msg
		wasStreamed = streamed
		lastErr = err

		if attempt < l.MaxRetries {
			backoff := l.RetryBackoff * time.Duration(1<<attempt)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return types.Message{}, false, ctx.Err()
			}
		}
	}
	return lastMsg, wasStreamed, lastErr
}

// captureUsage adds response token usage to the running total.
func (l *Loop) captureUsage(resp *types.ChatResponse) {
	l.TotalTokens.PromptTokens += resp.Usage.PromptTokens
	l.TotalTokens.CompletionTokens += resp.Usage.CompletionTokens
	l.TotalTokens.TotalTokens += resp.Usage.TotalTokens
}

// EstimatedTokens returns the estimated token count for all messages.
func (l *Loop) EstimatedTokens() int {
	total := 0
	for _, m := range l.Messages {
		total += len(m.Content)
		for _, tc := range m.ToolCalls {
			total += len(tc.Function.Arguments) + len(tc.Function.Name)
		}
	}
	return total / 4
}

// executeToolsParallel runs all tool calls concurrently and appends results
// in the original order.
func (l *Loop) executeToolsParallel(ctx context.Context, calls []types.ToolCall, messages *[]types.Message) {
	type result struct {
		idx     int
		callID  string
		name    string
		content string
		dur     time.Duration
		err     error
	}

	results := make(chan result, len(calls))

	for i, tc := range calls {
		i, tc := i, tc
		go func() {
			abbreviated := abbreviateArgs(tc.Function.Arguments, 80)

			if l.OnTool != nil {
				l.OnTool(ToolInfo{Name: tc.Function.Name, Args: abbreviated})
			}

			start := time.Now()
			res, err := l.Registry.Execute(ctx, tc.Function.Name, tc.Function.Arguments)
			duration := time.Since(start)

			if err != nil {
				res = fmt.Sprintf("error: %v", err)
			} else if len(res) > ToolResultMaxLen {
				res = res[:ToolResultMaxLen] + "\n...[truncated]..."
			}

			if l.OnTool != nil {
				info := ToolInfo{Name: tc.Function.Name, Args: abbreviated, Duration: duration, Result: res}
				if err != nil {
					info.Error = err.Error()
				}
				l.OnTool(info)
			}

			results <- result{idx: i, callID: tc.ID, name: tc.Function.Name, content: res, dur: duration, err: err}
		}()
	}

	ordered := make([]result, len(calls))
	for range calls {
		r := <-results
		ordered[r.idx] = r
	}

	for _, r := range ordered {
		*messages = append(*messages, types.ToolResultMsg(r.callID, r.name, r.content))
	}
}

// trimContext removes old messages when the estimated token count exceeds
// 80% of ContextWindow. Preserves the system message and recent exchanges.
func (l *Loop) trimContext() {
	target := l.ContextWindow * 4 / 5 // 80% threshold
	// Estimate tokens: ~4 chars per token
	totalChars := 0
	for _, m := range l.Messages {
		totalChars += len(m.Content)
		for _, tc := range m.ToolCalls {
			totalChars += len(tc.Function.Arguments) + len(tc.Function.Name)
		}
	}
	if totalChars/4 <= target {
		return
	}

	// Remove oldest non-system messages until we're under target
	sysMsg := l.Messages[0]
	rest := l.Messages[1:]
	for len(rest) > 0 && totalChars/4 > target {
		// Calculate chars for the oldest user-assistant exchange
		removed := len(rest[0].Content)
		for _, tc := range rest[0].ToolCalls {
			removed += len(tc.Function.Arguments) + len(tc.Function.Name)
		}
		totalChars -= removed
		rest = rest[1:]
	}

	newMsgs := make([]types.Message, 1, len(rest)+1)
	newMsgs[0] = sysMsg
	newMsgs = append(newMsgs, rest...)
	l.Messages = newMsgs
}

// executeTool runs a single tool call and appends the result to messages.
func (l *Loop) executeTool(ctx context.Context, tc types.ToolCall, messages *[]types.Message) {
	abbreviated := abbreviateArgs(tc.Function.Arguments, 80)

	if l.OnTool != nil {
		l.OnTool(ToolInfo{Name: tc.Function.Name, Args: abbreviated})
	}

	start := time.Now()
	result, err := l.Registry.Execute(ctx, tc.Function.Name, tc.Function.Arguments)
	duration := time.Since(start)

	if err != nil {
		result = fmt.Sprintf("error: %v", err)
	}

	if l.OnTool != nil {
		info := ToolInfo{Name: tc.Function.Name, Args: abbreviated, Duration: duration}
		if err != nil {
			info.Error = err.Error()
		}
		l.OnTool(info)
	}

	*messages = append(*messages, types.ToolResultMsg(tc.ID, tc.Function.Name, result))
}

// runStream handles a streaming request and returns the assembled assistant
// message (content + any tool calls). Tool calls accumulated from the stream
// are returned to Run, which executes them exactly like the non-streaming
// path. Content deltas are emitted via OnToken as they arrive.
func (l *Loop) runStream(ctx context.Context, sp StreamProvider, req types.ChatRequest) (types.Message, error) {
	chunks, errs := sp.SendStream(ctx, req)

	var content strings.Builder
	toolCallMap := make(map[int]*types.ToolCall)
	var finishReason string

	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				return l.assembleStreamed(content.String(), toolCallMap), nil
			}

			if len(chunk.Choices) == 0 {
				continue
			}

			delta := chunk.Choices[0].Delta

			if delta.ReasoningContent != "" && l.OnThinking != nil {
				l.OnThinking(delta.ReasoningContent)
			}

			if delta.Content != "" {
				content.WriteString(delta.Content)
				if l.OnToken != nil {
					l.OnToken(delta.Content)
				}
			}

			// Accumulate tool-call deltas by index.
			for _, tc := range delta.ToolCalls {
				idx := tc.Index
				if existing, ok := toolCallMap[idx]; ok {
					existing.Function.Arguments += tc.Function.Arguments
					if tc.ID != "" {
						existing.ID = tc.ID
					}
					if tc.Function.Name != "" {
						existing.Function.Name = tc.Function.Name
					}
				} else {
					newTC := types.ToolCall{
						Index: idx,
						ID:    tc.ID,
						Type:  tc.Type,
						Function: types.ToolCallFn{
							Name:      tc.Function.Name,
							Arguments: tc.Function.Arguments,
						},
					}
					toolCallMap[idx] = &newTC
				}
			}

			if chunk.Choices[0].FinishReason != nil {
				finishReason = *chunk.Choices[0].FinishReason
			}

		case err := <-errs:
			if err != nil {
				return types.Message{}, err
			}
			return l.assembleStreamed(content.String(), toolCallMap), nil

		case <-ctx.Done():
			return types.Message{}, ctx.Err()
		}

		if finishReason != "" {
			break
		}
	}

	return l.assembleStreamed(content.String(), toolCallMap), nil
}

// assembleStreamed builds the assistant message from accumulated stream state,
// ordering tool calls by their delta index.
func (l *Loop) assembleStreamed(content string, toolCalls map[int]*types.ToolCall) types.Message {
	msg := types.Message{
		Role:    "assistant",
		Content: content,
	}
	if len(toolCalls) > 0 {
		indices := make([]int, 0, len(toolCalls))
		for idx := range toolCalls {
			indices = append(indices, idx)
		}
		sort.Ints(indices)
		for _, idx := range indices {
			msg.ToolCalls = append(msg.ToolCalls, *toolCalls[idx])
		}
	}
	return msg
}

// buildToolDefs builds the OpenAI-format tool definitions from the registry.
func (l *Loop) buildToolDefs() []types.ToolDef {
	toolNames := l.Registry.List()
	toolDefs := make([]types.ToolDef, 0, len(toolNames))
	for _, name := range toolNames {
		t := l.Registry.Get(name)
		if t == nil {
			continue
		}
		toolDefs = append(toolDefs, types.ToolDef{
			Type: "function",
			Function: types.ToolFn{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  json.RawMessage(t.Schema()),
			},
		})
	}
	return toolDefs
}

// abbreviateArgs truncates JSON args to maxLen characters with ellipsis.
// Handles multi-byte UTF-8 by counting runes, not bytes.
func abbreviateArgs(args string, maxLen int) string {
	runes := []rune(args)
	if len(runes) <= maxLen {
		return args
	}
	return string(runes[:maxLen-3]) + "..."
}
