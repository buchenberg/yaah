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

	// Messages holds the conversation history across multiple Run calls.
	// When nil (first call), Run initializes it with the system prompt and
	// the current user input. When set by a preceding Run, subsequent calls
	// append the new user input and continue the conversation. After Run
	// returns (success or error), the caller can read this field to persist
	// the conversation for the next turn.
	Messages []types.Message
}

// Run executes the full conversation loop for a single user message.
// If l.Messages is non-nil, the conversation continues from where it
// left off (the user input is appended to the existing history). If nil,
// a new conversation is started with the system prompt.
func (l *Loop) Run(ctx context.Context, userInput string) (string, error) {
	if l.MaxIterations <= 0 {
		l.MaxIterations = 50
	}
	if l.Model == "" {
		l.Model = "gpt-4o-mini"
	}

	if l.Messages != nil {
		l.Messages = append(l.Messages, types.UserMsg(userInput))
	} else {
		l.Messages = []types.Message{
			types.SystemMsg(l.SystemPrompt),
			types.UserMsg(userInput),
		}
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

		// Get the next assistant message, preferring streaming when supported.
		msg, streamed, err := l.getAssistantMessage(ctx, req)
		if err != nil {
			l.Messages = messages
			return "", fmt.Errorf("provider error: %w", err)
		}
		messages = append(messages, msg)

		// No tool calls → this is the final answer.
		if len(msg.ToolCalls) == 0 {
			l.Messages = messages
			return msg.Content, nil
		}

		// If the message came from streaming, flush the accumulated content
		// to the UI so tool-call output starts on a fresh line.
		if streamed && msg.Content != "" && l.OnFlush != nil {
			l.OnFlush(msg.Content)
		}

		// Execute tool calls and append their results, then loop again.
		for _, tc := range msg.ToolCalls {
			l.executeTool(ctx, tc, &messages)
		}
	}

	l.Messages = messages
	return "", fmt.Errorf("max iterations (%d) reached without final response", l.MaxIterations)
}

// getAssistantMessage returns the next assistant message, preferring streaming
// when the provider supports it and an OnToken callback is set. The second
// return value is true when the message was assembled from a stream.
func (l *Loop) getAssistantMessage(ctx context.Context, req types.ChatRequest) (types.Message, bool, error) {
	if sp, ok := l.Provider.(StreamProvider); ok && l.OnToken != nil {
		msg, err := l.runStream(ctx, sp, req)
		return msg, true, err
	}
	msg, err := l.runNonStream(ctx, req)
	return msg, false, err
}

// runNonStream sends a non-streaming request and returns the assistant message.
func (l *Loop) runNonStream(ctx context.Context, req types.ChatRequest) (types.Message, error) {
	resp, err := l.Provider.Send(ctx, req)
	if err != nil {
		return types.Message{}, err
	}
	if len(resp.Choices) == 0 {
		return types.Message{}, fmt.Errorf("no choices in response")
	}
	return resp.Choices[0].Message, nil
}

// executeTool runs a single tool call and appends the result to messages.
func (l *Loop) executeTool(ctx context.Context, tc types.ToolCall, messages *[]types.Message) {
	abbreviated := abbreviateArgs(tc.Function.Arguments, 80)

	// Notify: tool call starting
	if l.OnTool != nil {
		l.OnTool(ToolInfo{
			Name: tc.Function.Name,
			Args: abbreviated,
		})
	}

	start := time.Now()
	result, err := l.Registry.Execute(ctx, tc.Function.Name, tc.Function.Arguments)
	duration := time.Since(start)

	if err != nil {
		result = fmt.Sprintf("error: %v", err)
	}

	// Notify: tool call complete
	if l.OnTool != nil {
		info := ToolInfo{
			Name:     tc.Function.Name,
			Args:     abbreviated,
			Duration: duration,
		}
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
				Description: "",
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
