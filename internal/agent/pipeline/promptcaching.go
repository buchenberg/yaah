package pipeline

import (
	"context"

	"github.com/buchenberg/yaah/internal/types"
)

const anthropicBreakpointCap = 4

// PromptCachingMiddleware adds Anthropic cache-control breakpoints to messages.
type PromptCachingMiddleware struct {
	enabled bool
}

func (m *PromptCachingMiddleware) Name() string { return "prompt_caching" }

func (m *PromptCachingMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	if !m.enabled {
		return step, nil
	}

	remaining := anthropicBreakpointCap

	// Priority 1: system message (always the first message).
	for i := range step.Messages {
		msg := &step.Messages[i]
		if msg.CacheControl != nil {
			continue
		}
		if msg.Role == "system" {
			if remaining <= 0 {
				break
			}
			msg.CacheControl = &types.CacheControl{Type: "ephemeral"}
			remaining--
			break
		}
	}

	// Priority 2: tool messages at turn boundaries, most recent first.
	for i := len(step.Messages) - 1; i >= 0; i-- {
		if remaining <= 0 {
			break
		}
		msg := &step.Messages[i]
		if msg.CacheControl != nil {
			continue
		}
		if msg.Role == "tool" && (i == len(step.Messages)-1 || step.Messages[i+1].Role == "user") {
			msg.CacheControl = &types.CacheControl{Type: "ephemeral"}
			remaining--
		}
	}

	return step, nil
}

func (m *PromptCachingMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	return step, nil
}

func (m *PromptCachingMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	return step, nil
}
