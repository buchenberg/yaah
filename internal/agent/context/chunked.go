package context

import (
	"github.com/buchenberg/yaah/internal/types"
)

const (
	// ChunkBudgetFraction is the fraction of the context window used as the
	// per-chunk token budget for chunked compaction.
	ChunkBudgetFraction = 0.6
	// MinChunkTokens is the floor for the per-chunk token budget.
	MinChunkTokens = 1000
	// MaxReduceDepth caps recursion when merging partial chunk summaries.
	MaxReduceDepth = 3
	// MaxChunkConcurrency bounds parallel chunk summarization calls.
	MaxChunkConcurrency = 3
)

// ChunkSplit divides messages into chunks that each fit within budget tokens.
func ChunkSplit(msgs []types.Message, budget int) [][]types.Message {
	if len(msgs) == 0 || budget <= 0 {
		return [][]types.Message{msgs}
	}

	var chunks [][]types.Message
	current := make([]types.Message, 0)
	currentTokens := 0

	for _, m := range msgs {
		msgTokens := MessageTokens(m)
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
