package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/types"
)

// EstimatedTokens returns the estimated token count for all messages.
func (l *Loop) EstimatedTokens() int {
	total := 0
	for _, m := range l.Messages {
		total += messageTokens(m)
	}
	return total
}

// messageTokens estimates the token count of a single message using chars/4
// for content plus tool-call arguments. Applies a 10-token floor for role/metadata.
func messageTokens(m types.Message) int {
	tokens := len(m.Content) / 4
	for _, tc := range m.ToolCalls {
		tokens += len(tc.Function.Arguments)/4 + len(tc.Function.Name)/4
	}
	if tokens < 10 {
		tokens = 10
	}
	return tokens
}

// truncateRunes slices s to at most maxLen runes, preserving head and tail
// with an ellipsis marker in between. Operates on rune boundaries to avoid
// corrupting multi-byte UTF-8 characters.
func truncateRunes(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	headLen := maxLen * 2 / 3
	tailLen := maxLen / 3
	return string(runes[:headLen]) + "\n...[truncated]...\n" + string(runes[len(runes)-tailLen:])
}

// pruneMessages replaces large tool and assistant messages with abbreviated
// markers to reduce token load before LLM summarization. Tool outputs (which
// also carry inner-executor summaries in the dual-loop path) become compact
// summary markers; assistant messages are truncated with rune-safe
// head+tail preservation.
func pruneMessages(msgs []types.Message, maxLen int) []types.Message {
	out := make([]types.Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
		if len(m.Content) <= maxLen {
			continue
		}
		switch m.Role {
		case "tool":
			lines := strings.Count(m.Content, "\n") + 1
			chars := len(m.Content)
			out[i].Content = fmt.Sprintf("[tool %s output — %d lines, %d chars]",
				m.Name, lines, chars)
		case "assistant":
			if len(m.ToolCalls) > 0 {
				continue
			}
			out[i].Content = truncateRunes(m.Content, maxLen)
		}
	}
	return out
}

// compactContext checks if the estimated token count exceeds the given
// fraction of ContextWindow. If threshold is 0, defaults to 0.5 (50%).
// If over budget, it uses the LLM to summarize old messages into a
// structured summary, preserving the system message and recent turns.
// Falls back to simple trimming if the LLM call fails or returns empty.
func (l *Loop) compactContext(ctx context.Context, threshold float64) {
	if l.ineffectiveCompactions >= 2 {
		return
	}

	if threshold <= 0 {
		threshold = 0.5
	}

	target := int(float64(l.ContextWindow) * threshold)
	if target < minContextFloor && l.ContextWindow >= minContextFloor {
		target = minContextFloor
	}

	estimatedTokens := l.LastPromptTokens
	if estimatedTokens <= 0 {
		estimatedTokens = l.EstimatedTokens()
	}

	if estimatedTokens < target {
		return
	}

	if len(l.Messages) <= 4 {
		return
	}

	sysMsg := l.Messages[0]
	rest := l.Messages[1:]

	keepCount := 8
	if l.PreviousSummary != "" {
		keepCount = 6
	}
	if keepCount > len(rest) {
		keepCount = len(rest)
	}

	keepMsgs := rest[len(rest)-keepCount:]
	oldMsgs := rest[:len(rest)-keepCount]
	oldMsgs = pruneMessages(oldMsgs, pruneMessageMaxLen)

	var sb strings.Builder
	if l.PreviousSummary != "" {
		sb.WriteString("Previous summary:\n")
		sb.WriteString(l.PreviousSummary)
		sb.WriteString("\n\n")
	}
	sb.WriteString("Conversation excerpt to summarize:\n\n")
	for _, m := range oldMsgs {
		if m.Content != "" {
			sb.WriteString(fmt.Sprintf("%s: %s\n", m.Role, m.Content))
		}
		for _, tc := range m.ToolCalls {
			sb.WriteString(fmt.Sprintf("[tool:%s] %s\n", tc.Function.Name, tc.Function.Arguments))
		}
	}

	compactProvider := l.CompactProvider
	if compactProvider == nil {
		compactProvider = l.Provider
	}
	compactModel := l.CompactModel
	if compactModel == "" {
		compactModel = l.Model
	}

	req := types.ChatRequest{
		Model:     compactModel,
		MaxTokens: 4096,
		Messages: []types.Message{
			types.UserMsg(sb.String()),
		},
	}

	beforeEstimate := l.EstimatedTokens()
	resp, err := compactProvider.Send(ctx, req)
	if err != nil || len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		l.trimContext()
		return
	}

	summary := resp.Choices[0].Message.Content
	l.PreviousSummary = summary

	newMsgs := []types.Message{sysMsg}
	if l.SystemPrompt == "" {
		newMsgs[0] = types.SystemMsg(summary)
	} else {
		newMsgs = append(newMsgs, types.SystemMsg("Previous conversation summary:\n"+summary))
	}
	newMsgs = append(newMsgs, keepMsgs...)
	l.Messages = newMsgs

	afterEstimate := l.EstimatedTokens()
	if beforeEstimate > 0 {
		savings := float64(beforeEstimate-afterEstimate) / float64(beforeEstimate)
		if savings < 0.10 {
			l.ineffectiveCompactions++
		} else {
			l.ineffectiveCompactions = 0
		}
	}
	l.lastCompactionTokens = afterEstimate
}

// trimContext removes old messages when the estimated token count exceeds
// 80% of ContextWindow. Preserves the system message and recent exchanges.
// This is a fallback when LLM-powered compaction is unavailable.
func (l *Loop) trimContext() {
	target := l.ContextWindow * 4 / 5
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

	sysMsg := l.Messages[0]
	rest := l.Messages[1:]
	for len(rest) > 0 && totalChars/4 > target {
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
