package agent

import (
	"context"

	"github.com/buchenberg/yaah/internal/types"
)

// PromptCachingMiddleware adds Anthropic cache-control breakpoints to messages
// so that repeated system prompts and tool results are cached between turns.
// This is a no-op for non-Anthropic providers.
type PromptCachingMiddleware struct {
	enabled bool
}

func (m *PromptCachingMiddleware) Name() string { return "prompt_caching" }

func (m *PromptCachingMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	if !m.enabled {
		return step, nil
	}

	for i := range step.Messages {
		msg := &step.Messages[i]
		if msg.CacheControl != nil {
			continue
		}
		switch msg.Role {
		case "system":
			msg.CacheControl = &types.CacheControl{Type: "ephemeral"}
		case "tool":
			if i == len(step.Messages)-1 || step.Messages[i+1].Role == "user" {
				msg.CacheControl = &types.CacheControl{Type: "ephemeral"}
			}
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
