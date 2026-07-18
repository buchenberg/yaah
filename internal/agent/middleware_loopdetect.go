package agent

import (
	"context"
	"fmt"

	"github.com/buchenberg/yaah/internal/types"
)

// LoopDetectionMiddleware tracks tool call hashes to detect stuck loops.
type LoopDetectionMiddleware struct {
	history *[]string
	count   int
	window  int
}

func (m *LoopDetectionMiddleware) Name() string { return "loop_detection" }

func (m *LoopDetectionMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	return step, nil
}

func (m *LoopDetectionMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	return step, nil
}

func (m *LoopDetectionMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	if m.history == nil {
		return step, nil
	}

	for _, r := range results {
		hashKey := toolCallHash(r.Name, r.Result)
		*m.history = append(*m.history, hashKey)

		if len(*m.history) > m.window {
			*m.history = (*m.history)[len(*m.history)-m.window:]
		}

		count := 0
		for _, h := range *m.history {
			if h == hashKey {
				count++
			}
		}
		if count >= m.count {
			return step, fmt.Errorf("loop detected: tool %q produced the same result %d times in the last %d steps — halting to prevent stuck agent",
				r.Name, count, len(*m.history))
		}
	}
	return step, nil
}
