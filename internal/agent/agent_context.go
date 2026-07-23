package agent

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/types"
)

const defaultEstimateFactor = 1.3

// Token-budget clamp for the preserved tail after compaction. The budget is
// 25% of the context window, clamped to [minPreserveTokens, maxPreserveTokens]
// so huge windows don't over-preserve and small windows keep a usable floor.
const (
	minPreserveTokens = 2000
	maxPreserveTokens = 8000
)

// summaryTemplate is the structured Markdown prompt sent to the compact
// provider. Forcing a fixed section order keeps summaries actionable and
// consistent across re-compactions. Ported from kilocode's anchored summary.
const summaryTemplate = `Output exactly the Markdown structure shown inside <template> and keep the section order unchanged. Do not include the <template> tags in your response.
<template>
## Goal
- [single-sentence task summary]

## Constraints & Preferences
- [user constraints, preferences, specs, or "(none)"]

## Progress
### Done
- [completed work or "(none)"]

### In Progress
- [current work or "(none)"]

### Blocked
- [blockers or "(none)"]

## Key Decisions
- [decision and why, or "(none)"]

## Next Steps
- [ordered next actions or "(none)"]

## Critical Context
- [important technical facts, errors, open questions, or "(none)"]

## Relevant Files
- [file or directory path: why it matters, or "(none)"]
</template>

Rules:
- Keep every section, even when empty.
- Use terse bullets, not prose paragraphs.
- Preserve exact file paths, commands, error strings, and identifiers when known.
- Do not mention the summary process or that context was compacted.`

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

// preflightTokens estimates the token count for a request payload (messages +
// tools) with a configurable multiplier to compensate for provider tokenizer
// undercounting (especially for code and JSON). The factor parameter defaults
// to 1.3 (defaultEstimateFactor) and is configurable via EstimateFactor on the
// Loop. Ported from kilocode overflow.ts:8,71.
func preflightTokens(messages []types.Message, tools []types.ToolDef, factor float64) int {
	total := 0
	for _, m := range messages {
		total += messageTokens(m)
	}
	for _, t := range tools {
		total += len(t.Function.Description)/4 + len(t.Function.Parameters)/4 + 10
	}
	if factor <= 0 {
		factor = defaultEstimateFactor
	}
	return int(math.Ceil(float64(total) * factor))
}

// isContinuation returns true if the conversation is mid-tool-loop (there are
// tool messages after the last user message). Compaction should be skipped in
// this case — the model needs the context to continue the tool loop.
// Ported from kilocode overflow.ts:17-20.
func isContinuation(messages []types.Message) bool {
	lastUserIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx < 0 {
		return false
	}
	for i := lastUserIdx + 1; i < len(messages); i++ {
		if messages[i].Role == "tool" {
			return true
		}
	}
	return false
}

// turnRange identifies a contiguous turn: a user message followed by its
// assistant response and any tool results, ending at the next user message
// (or end of conversation).
type turnRange struct {
	start int // index of the user message that starts this turn
	end   int // exclusive: index of the next user message or len(messages)
}

// turns segments messages into turn ranges starting at non-index-0 user
// messages. A "user" message at index 0 is the system prompt, not a turn.
// Messages before the first real user message are not in any turn.
// Ported from kilocode compaction.ts:161-177.
func turns(messages []types.Message) []turnRange {
	var result []turnRange
	for i, m := range messages {
		if m.Role != "user" || i == 0 {
			continue
		}
		result = append(result, turnRange{start: i, end: len(messages)})
	}
	for i := 0; i < len(result)-1; i++ {
		result[i].end = result[i+1].start
	}
	return result
}

// preserveBudget returns the token budget for the preserved tail: 25% of the
// context window, clamped to [minPreserveTokens, maxPreserveTokens]. Ported
// from kilocode compaction.ts:152-158 (preserveRecentBudget).
func preserveBudget(contextWindow int) int {
	budget := contextWindow / 4 // 25%
	if budget < minPreserveTokens {
		budget = minPreserveTokens
	}
	if budget > maxPreserveTokens {
		budget = maxPreserveTokens
	}
	return budget
}

// splitResult describes a compaction split: messages before keepStart are
// summarized, messages from keepStart onward are preserved verbatim.
type splitResult struct {
	keepStart int // index into messages where the tail begins
}

// splitTail finds the split point that keeps the most recent turns within the
// preserve budget, without splitting a tool-call/result pair. Walks backwards
// over turns, accumulating token sizes until the budget is exceeded. If a
// single turn exceeds the remaining budget, walks forward within that turn to
// find the earliest message that fits. Ported from kilocode compaction.ts:179-202.
func splitTail(messages []types.Message, budget int) splitResult {
	allTurns := turns(messages)
	if len(allTurns) == 0 {
		return splitResult{keepStart: len(messages)} // nothing to split
	}

	// Walk backwards over the most recent turns.
	total := 0
	keepStart := len(messages)
	for i := len(allTurns) - 1; i >= 0; i-- {
		t := allTurns[i]
		turnTokens := 0
		for j := t.start; j < t.end; j++ {
			turnTokens += messageTokens(messages[j])
		}
		if total+turnTokens <= budget {
			total += turnTokens
			keepStart = t.start
			continue
		}
		// Turn doesn't fit entirely — try to split it.
		if splitAt := splitTurn(messages, t, budget-total); splitAt >= 0 {
			keepStart = splitAt
		}
		break
	}

	// Never summarize past the first message (system prompt protection).
	if keepStart < 1 {
		keepStart = 1
	}
	return splitResult{keepStart: keepStart}
}

// splitTurn finds the earliest message within a turn that fits within the
// remaining budget. Returns -1 if no split is possible (entire turn is too
// large or the turn has only one message). Ported from kilocode compaction.ts:203-218.
func splitTurn(messages []types.Message, t turnRange, budget int) int {
	if budget <= 0 || t.end-t.start <= 1 {
		return -1
	}
	for start := t.start + 1; start < t.end; start++ {
		size := 0
		for j := start; j < t.end; j++ {
			size += messageTokens(messages[j])
		}
		if size <= budget {
			return start
		}
	}
	return -1
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
// markers to reduce token load before LLM summarization. Tool outputs become
// compact summary markers; assistant messages are truncated with rune-safe
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
// fraction of ContextWindow. If threshold is 0, defaults to 0.25 (25%).
// If over budget, it uses the LLM to summarize old messages into a
// structured summary, preserving the system message and recent turns.
// Falls back to simple trimming if the LLM call fails or returns empty.
//
// Preflight: when LastPromptTokens is 0 (first call), uses preflightTokens
// with the configurable EstimateFactor (default 1.3) to estimate tokens.
// Cache subtraction: when LastCachedPromptTokens > 0, it is subtracted from
// LastPromptTokens so heavily-cached conversations don't over-trigger
// compaction (cached tokens are effectively free at the provider).
func (l *Loop) compactContext(ctx context.Context, threshold float64) {
	if l.ineffectiveCompactions >= 2 {
		return
	}

	if l.SessionID != "" && l.DB != nil {
		cooldown, ineffective, err := l.DB.GetCompactionCooldown(l.SessionID)
		if err == nil && cooldown > 0 && time.Now().Unix() < cooldown {
			return
		}
		if err == nil && ineffective > l.ineffectiveCompactions {
			l.ineffectiveCompactions = ineffective
		}
	}

	if threshold <= 0 {
		threshold = 0.25
	}

	target := int(float64(l.ContextWindow) * threshold)
	if target < minContextFloor && l.ContextWindow >= minContextFloor {
		target = minContextFloor
	}

	estimatedTokens := l.LastPromptTokens
	// Subtract cached prompt tokens: a heavily-cached conversation's effective
	// (non-cached) token cost is lower than raw prompt_tokens suggests, so
	// without subtraction the compactor would over-trigger.
	if estimatedTokens > 0 && l.LastCachedPromptTokens > 0 {
		estimatedTokens -= l.LastCachedPromptTokens
	}
	if estimatedTokens <= 0 {
		factor := l.EstimateFactor
		if factor <= 0 {
			factor = defaultEstimateFactor
		}
		estimatedTokens = preflightTokens(l.Messages, nil, factor)
	}

	if estimatedTokens < target {
		return
	}

	if len(l.Messages) <= 4 {
		return
	}

	sysMsg := l.Messages[0]

	// Token-budgeted survival split (replaces fixed keepCount): keeps the
	// most recent turns within the preserve budget without splitting a
	// tool-call/result pair.
	//
	// messageTokens uses chars/4 which consistently undercounts relative to
	// the actual tokenizer count (often by 2-4x for code/JSON-heavy payloads).
	// The budget derived from preserveBudget(window) is in tokenizer units,
	// while splitTail sums messageTokens (chars/4), so without scaling every
	// conversation appears to fit. Scale the budget by the actual ratio of
	// estimatedTokens (tokenizer) to total messageTokens (chars/4) so the
	// compaction decision and the split decision use the same yardstick.
	budget := preserveBudget(l.ContextWindow)
	if estimatedTokens > 0 {
		msgTotal := 0
		for _, m := range l.Messages {
			msgTotal += messageTokens(m)
		}
		if msgTotal > 0 {
			scale := float64(estimatedTokens) / float64(msgTotal)
			budget = int(float64(budget) / scale)
		}
	}
	split := splitTail(l.Messages, budget)
	keepMsgs := l.Messages[split.keepStart:]
	oldMsgs := l.Messages[1:split.keepStart]
	oldMsgs = pruneMessages(oldMsgs, pruneMessageMaxLen)

	// Structured summary prompt with anchored-update behavior on re-compaction.
	var sb strings.Builder
	if l.PreviousSummary != "" {
		sb.WriteString("Update the anchored summary below using the conversation history above.\n")
		sb.WriteString("Preserve still-true details, remove stale details, and merge in the new facts.\n")
		sb.WriteString("<previous-summary>\n")
		sb.WriteString(l.PreviousSummary)
		sb.WriteString("\n</previous-summary>\n\n")
	} else {
		sb.WriteString("Create a new anchored summary from the conversation history below.\n\n")
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
	sb.WriteString("\n\n")
	sb.WriteString(summaryTemplate)

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
	l.resetPruner()

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

	if l.SessionID != "" && l.DB != nil {
		cooldown := int64(0)
		if l.ineffectiveCompactions >= 2 {
			cooldown = time.Now().Unix() + 600
		}
		l.DB.SetCompactionCooldown(l.SessionID, cooldown, l.ineffectiveCompactions)
	}
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
	l.resetPruner()
}
