package agent

import (
	"github.com/buchenberg/yaah/internal/types"
)

const (
	chunkBudgetFraction = 0.6
	minChunkTokens      = 1000
	maxReduceDepth      = 3
	maxChunkConcurrency = 3
)

// chunkSplit divides messages into chunks that each fit within budget tokens.
func chunkSplit(msgs []types.Message, budget int) [][]types.Message {
	if len(msgs) == 0 || budget <= 0 {
		return [][]types.Message{msgs}
	}

	var chunks [][]types.Message
	current := make([]types.Message, 0)
	currentTokens := 0

	for _, m := range msgs {
		msgTokens := messageTokens(m)
		if currentTokens+msgTokens > budget && len(current) > 0 {
			chunks = append(chunks, current)
			current = make([]types.Message, 0)
			currentTokens = 0
		}
		current = append(current, m)
		currentTokens += msgTokens
	}

	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	if len(chunks) == 0 {
		chunks = append(chunks, nil)
	}
	return chunks
}

