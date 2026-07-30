package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/types"
)

const (
	// chunkBudgetFraction is the fraction of the compact model's context
	// window to use per chunk.
	chunkBudgetFraction = 0.6

	// minChunkTokens is the minimum number of tokens a chunk must contain
	// to be worth summarizing independently.
	minChunkTokens = 1000

	// maxReduceDepth limits the recursive merging depth to prevent runaway
	// summarization cascades.
	maxReduceDepth = 3

	// maxChunkConcurrency limits the number of parallel chunk summarizations.
	maxChunkConcurrency = 3
)

// chunkSplit divides messages into chunks that each fit within budget tokens.
// It uses a greedy accumulation: walk forward, adding messages to the current
// chunk until adding the next message would exceed the budget, then start a
// new chunk. Returns at least one chunk (possibly empty if budget ≤ 0).
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

// summarizeChunk sends a single chunk to the compact model for summarization.
// It uses the same structured summary prompt as the main compaction path.
func (l *Loop) summarizeChunk(ctx context.Context, chunk []types.Message, chunkIdx, total int) (string, error) {
	if len(chunk) == 0 {
		return "", nil
	}

	provider := l.CompactProvider
	if provider == nil {
		provider = l.Provider
	}
	model := l.CompactModel
	if model == "" {
		model = l.Model
	}

	var sb strings.Builder
	sb.WriteString(prompts.ChunkPreamble(chunkIdx+1, total) + "\n\n")
	sb.WriteString("<conversation>\n")
	for _, m := range chunk {
		if m.Content != "" {
			if m.Role == "tool" {
				sb.WriteString(formatToolStub(m) + "\n")
			} else {
				sb.WriteString(fmt.Sprintf("%s: %s\n", m.Role, m.Content))
			}
		}
		for _, tc := range m.ToolCalls {
			sb.WriteString(fmt.Sprintf("[tool_call:%s] %s\n", tc.Function.Name, tc.Function.Arguments))
		}
	}
	sb.WriteString("</conversation>\n\n")
	sb.WriteString(prompts.SummaryTemplate())

	req := types.ChatRequest{
		Model:     model,
		MaxTokens: 4096,
		Messages: []types.Message{
			types.SystemMsg(prompts.ChunkSummarizerRole()),
			types.UserMsg(sb.String()),
		},
	}

	resp, err := provider.Send(ctx, req)
	if err != nil {
		return "", fmt.Errorf("chunk %d/%d summarization failed: %w", chunkIdx+1, total, err)
	}

	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		return "", nil
	}

	return resp.Choices[0].Message.Content, nil
}

// chunkedCompact performs multi-pass chunked summarization of old messages.
// It splits messages into chunks, summarizes each in parallel (with bounded
// concurrency), then recursively merges partial summaries until a single
// final summary is produced.
func (l *Loop) chunkedCompact(ctx context.Context, oldMsgs []types.Message, compactModel string) (string, error) {
	if len(oldMsgs) == 0 {
		return "", nil
	}

	chunkBudget := int(float64(l.ContextWindow) * chunkBudgetFraction)
	if chunkBudget < minChunkTokens {
		chunkBudget = minChunkTokens
	}

	chunks := chunkSplit(oldMsgs, chunkBudget)
	if len(chunks) <= 1 {
		return l.summarizeChunk(ctx, oldMsgs, 0, 1)
	}

	// Summarize chunks in parallel with bounded concurrency.
	sem := make(chan struct{}, maxChunkConcurrency)
	var wg sync.WaitGroup
	results := make([]string, len(chunks))
	errors := make([]error, len(chunks))

	for i, chunk := range chunks {
		wg.Add(1)
		go func(idx int, c []types.Message) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			results[idx], errors[idx] = l.summarizeChunk(ctx, c, idx, len(chunks))
		}(i, chunk)
	}
	wg.Wait()

	// Collect successful summaries.
	var partials []string
	for i, r := range results {
		if errors[i] != nil {
			continue
		}
		if strings.TrimSpace(r) != "" {
			partials = append(partials, r)
		}
	}

	if len(partials) == 0 {
		return "", fmt.Errorf("all chunk summarizations failed")
	}

	if len(partials) == 1 {
		return partials[0], nil
	}

	// Recursive merge of partial summaries.
	return l.reducePartialSummaries(ctx, partials, 1, compactModel)
}

// reducePartialSummaries recursively merges partial summaries into a single
// coherent summary. Uses the compact model to combine them.
func (l *Loop) reducePartialSummaries(ctx context.Context, partials []string, depth int, compactModel string) (string, error) {
	if len(partials) == 1 {
		return partials[0], nil
	}
	if len(partials) == 0 {
		return "", nil
	}
	if depth > maxReduceDepth {
		// Safety valve: concatenate instead of deeper recursion.
		return strings.Join(partials, "\n###\n"), nil
	}

	provider := l.CompactProvider
	if provider == nil {
		provider = l.Provider
	}
	model := l.CompactModel
	if model == "" {
		model = l.Model
	}

	var sb strings.Builder
	sb.WriteString(prompts.ChunkMergerPreamble() + "\n\n")
	for i, p := range partials {
		sb.WriteString(fmt.Sprintf("<partial-summary-%d>\n%s\n</partial-summary-%d>\n\n", i+1, p, i+1))
	}
	sb.WriteString(prompts.SummaryTemplate())

	req := types.ChatRequest{
		Model:     model,
		MaxTokens: 4096,
		Messages: []types.Message{
			types.SystemMsg(prompts.ChunkMergerRole()),
			types.UserMsg(sb.String()),
		},
	}

	resp, err := provider.Send(ctx, req)
	if err != nil {
		return strings.Join(partials, "\n###\n"), nil
	}

	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		return strings.Join(partials, "\n###\n"), nil
	}

	merged := resp.Choices[0].Message.Content

	// If merged result is still multiple conceptual summaries, recurse.
	// Use a simple heuristic: if the merged text is > 80% of the combined
	// length, it likely didn't reduce much — try again at next depth.
	combinedLen := 0
	for _, p := range partials {
		combinedLen += len(p)
	}
	if float64(len(merged)) > float64(combinedLen)*0.8 {
		// Minimal reduction; split into two and try shallower merge.
		mid := len(partials) / 2
		left, err := l.reducePartialSummaries(ctx, partials[:mid], depth+1, compactModel)
		if err != nil {
			return merged, nil
		}
		right, err := l.reducePartialSummaries(ctx, partials[mid:], depth+1, compactModel)
		if err != nil {
			return merged, nil
		}
		return l.reducePartialSummaries(ctx, []string{left, right}, depth+1, compactModel)
	}

	return merged, nil
}
