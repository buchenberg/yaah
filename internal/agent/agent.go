package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// Provider is the interface for model backends.
type Provider interface {
	Send(req types.ChatRequest) (*types.ChatResponse, error)
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
}

// Run executes the full conversation loop for a single user message.
func (l *Loop) Run(ctx context.Context, userInput string) (string, error) {
	if l.MaxIterations <= 0 {
		l.MaxIterations = 50
	}
	if l.Model == "" {
		l.Model = "gpt-4o-mini"
	}

	messages := []types.Message{
		types.SystemMsg(l.SystemPrompt),
		types.UserMsg(userInput),
	}

	toolDefs := l.buildToolDefs()

	for iter := 0; iter < l.MaxIterations; iter++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		req := types.ChatRequest{
			Model:    l.Model,
			Messages: messages,
			Tools:    toolDefs,
		}

		// Try streaming first
		if sp, ok := l.Provider.(StreamProvider); ok && l.OnToken != nil {
			result, err := l.runStream(ctx, sp, req)
			if err == nil {
				return result, nil
			}
			// Fall through to non-streaming on error
		}

		// Non-streaming fallback
		resp, err := l.Provider.Send(req)
		if err != nil {
			return "", fmt.Errorf("provider error: %w", err)
		}

		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("no choices in response")
		}

		choice := resp.Choices[0]
		msg := choice.Message
		messages = append(messages, msg)

		if len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				l.executeTool(tc, &messages)
			}
			continue
		}

		return msg.Content, nil
	}

	return "", fmt.Errorf("max iterations (%d) reached without final response", l.MaxIterations)
}

// executeTool runs a single tool call and appends the result to messages.
func (l *Loop) executeTool(tc types.ToolCall, messages *[]types.Message) {
	abbreviated := abbreviateArgs(tc.Function.Arguments, 80)

	// Notify: tool call starting
	if l.OnTool != nil {
		l.OnTool(ToolInfo{
			Name: tc.Function.Name,
			Args: abbreviated,
		})
	}

	start := time.Now()
	result, err := l.Registry.Execute(tc.Function.Name, tc.Function.Arguments)
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

// runStream handles a streaming request.
func (l *Loop) runStream(ctx context.Context, sp StreamProvider, req types.ChatRequest) (string, error) {
	chunks, errs := sp.SendStream(ctx, req)

	var content strings.Builder
	var toolCalls []types.ToolCall
	toolCallMap := make(map[int]*types.ToolCall)
	var finishReason string

	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				if content.Len() > 0 && len(toolCalls) == 0 {
					return content.String(), nil
				}
				if len(toolCalls) > 0 {
					return "", fmt.Errorf("streaming tool calls not yet fully supported")
				}
				return content.String(), nil
			}

			if len(chunk.Choices) == 0 {
				continue
			}

			delta := chunk.Choices[0].Delta

			// Emit token callback
			if delta.Content != "" && l.OnToken != nil {
				l.OnToken(delta.Content)
			}
			content.WriteString(delta.Content)

			// Accumulate tool calls
			for _, tc := range delta.ToolCalls {
				idx := tc.Index
				if existing, ok := toolCallMap[idx]; ok {
					existing.Function.Arguments += tc.Function.Arguments
					if tc.ID != "" {
						existing.ID = tc.ID
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
				return "", err
			}
			if content.Len() > 0 {
				return content.String(), nil
			}
			return "", nil

		case <-ctx.Done():
			return "", ctx.Err()
		}

		if finishReason != "" {
			break
		}
	}

	// Collect tool calls
	for _, tc := range toolCallMap {
		toolCalls = append(toolCalls, *tc)
	}

	if len(toolCalls) > 0 {
		return "", fmt.Errorf("streaming tool calls not yet fully supported")
	}

	return content.String(), nil
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
func abbreviateArgs(args string, maxLen int) string {
	if len(args) <= maxLen {
		return args
	}
	return args[:maxLen-3] + "..."
}
