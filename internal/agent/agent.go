// Package agent implements the core conversation loop for yaah.
// It takes a provider client, a tool registry, a system prompt, and
// user input, then runs the OpenAI Chat Completions loop until the
// model responds with text (or hits the iteration limit).
package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// Provider is the interface for model backends. The OpenAI client
// in internal/providers satisfies this; test mocks do too.
type Provider interface {
	Send(req types.ChatRequest) (*types.ChatResponse, error)
}

// Loop runs the agent conversation loop.
type Loop struct {
	Provider      Provider
	Registry      *tools.Registry
	SystemPrompt  string
	Model         string
	MaxIterations int
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

		resp, err := l.Provider.Send(types.ChatRequest{
			Model:    l.Model,
			Messages: messages,
			Tools:    toolDefs,
		})
		if err != nil {
			return "", fmt.Errorf("provider error: %w", err)
		}

		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("no choices in response")
		}

		choice := resp.Choices[0]
		msg := choice.Message

		// Append assistant message to history
		messages = append(messages, msg)

		// If the model made tool calls, execute them and continue
		if len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				result, err := l.Registry.Execute(tc.Function.Name, tc.Function.Arguments)
				if err != nil {
					result = fmt.Sprintf("error: %v", err)
				}
				messages = append(messages, types.ToolResultMsg(tc.ID, tc.Function.Name, result))
			}
			continue
		}

		// Plain text response — we're done
		return msg.Content, nil
	}

	return "", fmt.Errorf("max iterations (%d) reached without final response", l.MaxIterations)
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
