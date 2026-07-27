package agent

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/types"
)

const defaultEstimateFactor = 1.3

// defaultRawCompactionThreshold is the fraction of ContextWindow at which
// compaction fires based on raw (non-cache-adjusted) prompt tokens. It guards
// against latency degradation in heavily-cached conversations where the
// effective-token trigger never fires. 0.5 matches hermes's 50% threshold.
const defaultRawCompactionThreshold = 0.5

// maxPayloadBytes is the serialized request size above which the payload-size
// guard forces compaction regardless of token estimates. Token heuristics
// (chars/4) can undercount code and JSON by 2-4x, so a byte-level check catches
// oversized payloads the token trigger misses. ~1.25MB matches kilocode's
// prompt.ts payload-limit prune threshold.
const maxPayloadBytes = 1_250_000

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

// lastUserPrompt returns the content of the most recent user message in the
// slice, or "" if none exists. Used by compaction to preserve the active task.
func lastUserPrompt(msgs []types.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && msgs[i].Content != "" {
			return msgs[i].Content
		}
	}
	return ""
}

// messageTokens estimates the token count of a single message using chars/4
// for content, reasoning content, plus tool-call arguments. Applies a 10-token
// floor for role/metadata. ReasoningContent is counted because it is serialized
// in every provider request and contributes to the real prompt size; omitting it
// causes token estimates (and therefore compaction triggers) to undercount.
func messageTokens(m types.Message) int {
	tokens := len(m.Content)/4 + len(m.ReasoningContent)/4
	for _, tc := range m.ToolCalls {
		tokens += len(tc.Function.Arguments)/4 + len(tc.Function.Name)/4
	}
	if tokens < 10 {
		tokens = 10
	}
	return tokens
}

// prepareRequestMessages builds the ephemeral message slice sent to the
// provider for a single turn. It chains the request-time transformations that
// must NOT mutate the stored conversation history (l.Messages):
//
//  1. repairOrphans — drop orphaned tool results, synthesize results for
//     interrupted tool calls (allocates a fresh slice).
//  2. applyPruning  — stub soft-pruned tool results (Tier-0 context reclaim).
//
// ReasoningContent is NOT stripped: thinking-mode providers (e.g. DeepSeek)
// require it to be passed back in every assistant message. Stripping it
// triggers a 400: "The reasoning_content in the thinking mode must be passed
// back to the API."
//
// Because repairOrphans always returns a new slice, the stored history is never
// mutated by any of these passes.
func (l *Loop) prepareRequestMessages(messages []types.Message) []types.Message {
	out := l.applyPruning(repairOrphans(messages))

	// Guard: verify no reasoning-carrying assistant messages were lost.
	// The compaction/trim pipeline must either preserve every reasoning
	// message or fold its content into a system message summary.
	if src := countReasoningMessages(messages); countReasoningMessages(out) < src {
		log.Printf("BUG: reasoning_content lost — %d reasoning msg(s) in source, %d in output (earliest idx: %d)", src, countReasoningMessages(out), EarliestReasoningIndex(messages))
	}

	return out
}

// countReasoningMessages returns the number of assistant messages that carry
// reasoning_content. The result is an exact count, not a turn count.
func countReasoningMessages(msgs []types.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == "assistant" && m.ReasoningContent != "" {
			n++
		}
	}
	return n
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

// estimatePayloadBytes estimates the serialized size of a chat request payload
// (messages plus tool definitions) in bytes. It backs the payload-size guard: a
// byte-level check catches oversized requests that the chars/4 token heuristic
// misses, since that heuristic systematically undercounts code and JSON.
func estimatePayloadBytes(messages []types.Message, tools []types.ToolDef) int {
	total := 0
	for _, m := range messages {
		total += len(m.Content) + len(m.ReasoningContent)
		for _, tc := range m.ToolCalls {
			total += len(tc.Function.Arguments) + len(tc.Function.Name) + len(tc.ID)
		}
	}
	for _, t := range tools {
		total += len(t.Function.Description) + len(t.Function.Parameters) + len(t.Function.Name)
	}
	return total
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

// ProtectReasoningTurns ensures compaction does not remove assistant messages
// that carry reasoning_content. Thinking-mode providers (e.g. DeepSeek) require
// EVERY reasoning-carrying assistant message to be passed back in every
// subsequent request. If compaction removes any, the next request gets a 400:
// "The reasoning_content in the thinking mode must be passed back to the API."
//
// protectTurns=0 disables protection entirely (explicit opt-out). Otherwise
// ALL reasoning-carrying messages in oldMsgs are protected, regardless of
// the configured count — DeepSeek requires every one.
func ProtectReasoningTurns(messages []types.Message, keepStart, protectTurns int) int {
	if protectTurns <= 0 || keepStart <= 1 {
		return keepStart
	}
	// Only protect reasoning in oldMsgs (messages before keepStart).
	// Reasoning already in keepMsgs is already preserved by compaction.
	if idx := EarliestReasoningIndex(messages[:keepStart]); idx > 0 && idx < keepStart {
		return idx
	}
	return keepStart
}

// EarliestReasoningIndex scans the message slice for assistant messages that
// carry reasoning_content, finds the earliest (oldest) one, and returns the
// index of its enclosing user message (or 1 if the user message is at index 0).
// Returns 0 if no reasoning-carrying messages exist.
//
// This is the single source of truth for reasoning-content protection. Every
// code path that removes messages from the conversation history must ensure
// it preserves messages from this index onward, or the next request to a
// thinking-mode provider will fail with a 400 error.
func EarliestReasoningIndex(messages []types.Message) int {
	for i := 1; i < len(messages); i++ {
		if messages[i].Role == "assistant" && messages[i].ReasoningContent != "" {
			for j := i - 1; j >= 0; j-- {
				if messages[j].Role == "user" || j == 0 {
					if j == 0 {
						return 1
					}
					return j
				}
			}
			return i
		}
	}
	return 0
}

// applyCompactedSummary replaces the message list with the compacted summary
// plus kept messages. It is called by both the normal and chunked compaction
// paths so they share the same post-compaction logic.
func (l *Loop) applyCompactedSummary(summary string, sysMsg types.Message, oldMsgs, keepMsgs []types.Message) {
	l.PreviousSummary = summary

	newMsgs := []types.Message{sysMsg}
	if l.SystemPrompt == "" {
		newMsgs[0] = types.SystemMsg(summary)
	} else {
		newMsgs = append(newMsgs, types.SystemMsg("Previous conversation summary:\n"+summary))
	}

	// Preserve the most recent user prompt verbatim so the model retains
	// its active task even after compaction summarizes older context.
	if lastUser := lastUserPrompt(oldMsgs); lastUser != "" {
		alreadyKept := false
		for _, m := range keepMsgs {
			if m.Role == "user" && m.Content == lastUser {
				alreadyKept = true
				break
			}
		}
		if !alreadyKept {
			newMsgs = append(newMsgs, types.SystemMsg("Active task (preserve verbatim):\n"+lastUser))
		}
	}

	newMsgs = append(newMsgs, keepMsgs...)
	beforeEstimate := l.EstimatedTokens()
	l.Messages = newMsgs
	l.resetPruner()
	if l.Pruner != nil {
		l.Pruner.Mark(l.Messages, "post_compaction")
	}

	afterEstimate := l.EstimatedTokens()
	if beforeEstimate > 0 {
		savings := float64(beforeEstimate-afterEstimate) / float64(beforeEstimate)
		if savings < 0.10 {
			l.ineffectiveCompactions++
		} else {
			l.ineffectiveCompactions = 0
		}
		l.trackCompactionSavings(savings)
	}
	l.lastCompactionTokens = afterEstimate
}

const adaptiveSavingsWindow = 5

func (l *Loop) trackCompactionSavings(savings float64) {
	l.compactionSavingsHistory = append(l.compactionSavingsHistory, savings)
	if len(l.compactionSavingsHistory) > adaptiveSavingsWindow {
		l.compactionSavingsHistory = l.compactionSavingsHistory[1:]
	}

	highCount := 0
	lowCount := 0
	for _, s := range l.compactionSavingsHistory {
		if s > 0.4 {
			highCount++
		}
		if s < 0.1 {
			lowCount++
		}
	}

	if highCount >= 3 {
		l.compactionBudgetMultiplier *= 0.9
		l.compactionSavingsHistory = nil
	}
	if lowCount >= 2 {
		l.compactionBudgetMultiplier *= 1.2
		l.compactionSavingsHistory = nil
	}
	if l.compactionBudgetMultiplier < 0.5 {
		l.compactionBudgetMultiplier = 0.5
	}
	if l.compactionBudgetMultiplier > 2.0 {
		l.compactionBudgetMultiplier = 2.0
	}
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

// formatToolStub produces a compact, structured summary of a tool result
// message for the compaction serializer. Instead of embedding the full output
// (which can be thousands of lines of grep/cat/ls results), it emits a stub
// with line count and the first meaningful snippet.
func formatToolStub(m types.Message) string {
	content := strings.TrimSpace(m.Content)
	if content == "" {
		return fmt.Sprintf("[tool:%s (empty output)]", m.Name)
	}
	lines := strings.Count(content, "\n") + 1
	chars := len(content)
	firstLine := content
	if idx := strings.IndexByte(content, '\n'); idx > 0 {
		firstLine = content[:idx]
	}
	if len(firstLine) > 120 {
		firstLine = firstLine[:120] + "..."
	}
	return fmt.Sprintf("[tool:%s — %d lines, %d chars, starts: %q]", m.Name, lines, chars, firstLine)
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
	// Self-reset: two successive low-savings compactions latch the guard off
	// (ineffectiveCompactions >= 2). That verdict is only valid for the context
	// size at the time of the last attempt. If the context has since grown by
	// >= 50%, retry — otherwise compaction stays permanently disabled even as
	// the conversation bloats (the catch-22 seen in long sessions where the
	// pruner alone could not keep context bounded).
	if l.ineffectiveCompactions >= 2 && l.lastCompactionTokens > 0 {
		if est := l.EstimatedTokens(); est >= l.lastCompactionTokens*3/2 {
			l.ineffectiveCompactions = 0
			if l.SessionID != "" && l.DB != nil {
				l.DB.SetCompactionCooldown(l.SessionID, 0, 0)
			}
		}
	}

	if l.ineffectiveCompactions >= 2 {
		return
	}

	if l.SessionID != "" && l.DB != nil {
		cooldown, ineffective, err := l.DB.GetCompactionCooldown(l.SessionID)
		if err == nil && cooldown > 0 && time.Now().Unix() < cooldown {
			return
		}
		if err == nil && ineffective != l.ineffectiveCompactions {
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

	// Raw prompt tokens from the most recent provider call — the total context
	// size the provider actually processed, before any cache adjustment.
	rawTokens := l.LastPromptTokens

	// Effective tokens: subtract cached prompt tokens. A heavily-cached
	// conversation's effective (non-cached) token cost is lower than raw
	// prompt_tokens suggests, so without subtraction the cost-based trigger
	// would over-compact.
	effectiveTokens := rawTokens
	if effectiveTokens > 0 && l.LastCachedPromptTokens > 0 {
		effectiveTokens -= l.LastCachedPromptTokens
	}
	if effectiveTokens <= 0 {
		factor := l.EstimateFactor
		if factor <= 0 {
			factor = defaultEstimateFactor
		}
		effectiveTokens = preflightTokens(l.Messages, nil, factor)
		// No reliable raw count on the first call; use the estimate for the
		// raw trigger too so the latency guard still functions.
		rawTokens = effectiveTokens
	}

	// Dual trigger: compact on either effective tokens (cost) exceeding the
	// CompactionThreshold target, OR raw tokens (latency) exceeding the
	// RawCompactionThreshold target. The raw guard prevents unbounded context
	// growth in heavily-cached conversations where the effective trigger never
	// fires because cached tokens keep the effective count artificially low.
	rawThreshold := l.RawCompactionThreshold
	if rawThreshold <= 0 {
		rawThreshold = defaultRawCompactionThreshold
	}
	rawTarget := int(float64(l.ContextWindow) * rawThreshold)

	if effectiveTokens < target && rawTokens < rawTarget {
		return
	}

	if len(l.Messages) <= 4 {
		return
	}

	// Determine compaction reason for event consumers.
	compactReason := "threshold"
	if l.compactionForcedByOverflow {
		compactReason = "overflow"
		l.compactionForcedByOverflow = false
	}

	if l.broker != nil {
		l.broker.Publish(&CompactionStartedEvent{
			BeforeTokens: rawTokens,
			TargetTokens: target,
			Reason:       compactReason,
		})
	}

	startTime := time.Now()

	if l.OtelEnabled {
		_, span := otel.Tracer("yaah").Start(ctx, "compaction",
			trace.WithAttributes(
				attribute.Int("compaction.effective_tokens", effectiveTokens),
				attribute.Int("compaction.raw_tokens", rawTokens),
				attribute.Int("compaction.cached_tokens", l.LastCachedPromptTokens),
				attribute.Int("compaction.target", target),
				attribute.Int("compaction.raw_target", rawTarget),
				attribute.Int("compaction.messages", len(l.Messages)),
			))
		defer span.End()
	}

	sysMsg := l.Messages[0]

	budget := int(float64(preserveBudget(l.ContextWindow))*l.compactionBudgetMultiplier) / 4
	split := splitTail(l.Messages, budget)
	keepMsgs := l.Messages[split.keepStart:]
	oldMsgs := l.Messages[1:split.keepStart]

	// Protect assistant messages that carry reasoning_content: thinking-mode
	// providers (e.g. DeepSeek) require reasoning_content to be passed back
	// in every request for all assistant messages that have it. If compaction
	// removes a reasoning-carrying message, the next request gets a 400:
	// "The reasoning_content in the thinking mode must be passed back to the API."
	if l.ReasoningProtectTurns > 0 {
		split.keepStart = ProtectReasoningTurns(l.Messages, split.keepStart, l.ReasoningProtectTurns)
		keepMsgs = l.Messages[split.keepStart:]
		oldMsgs = l.Messages[1:split.keepStart]
	}
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
			if m.Role == "tool" {
				sb.WriteString(formatToolStub(m) + "\n")
			} else {
				sb.WriteString(fmt.Sprintf("%s: %s\n", m.Role, m.Content))
			}
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
		// Primary compaction failed — try chunked fallback before giving up.
		if len(oldMsgs) > minChunkTokens {
			if chunkSummary, chunkErr := l.chunkedCompact(ctx, oldMsgs, compactModel); chunkErr == nil && chunkSummary != "" {
				l.applyCompactedSummary(chunkSummary, sysMsg, oldMsgs, keepMsgs)
				return
			}
		}
		l.trimContext()
		return
	}

	summary := resp.Choices[0].Message.Content
	l.applyCompactedSummary(summary, sysMsg, oldMsgs, keepMsgs)
	afterEstimate := l.EstimatedTokens()
	savingsPct := 0.0
	ineffectiveNote := ""
	if beforeEstimate > 0 {
		savingsPct = float64(beforeEstimate-afterEstimate) / float64(beforeEstimate)
	}

	if l.ineffectiveCompactions >= 2 {
		ineffectiveNote = fmt.Sprintf("compaction ineffective %d times; cooldown active", l.ineffectiveCompactions)
	}

	if l.broker != nil {
		l.broker.Publish(&CompactionDoneEvent{
			BeforeTokens:    beforeEstimate,
			AfterTokens:     afterEstimate,
			SavingsPct:      savingsPct,
			Method:          "single",
			ElapsedSeconds:  time.Since(startTime).Seconds(),
			IneffectiveNote: ineffectiveNote,
		})
	}

	if l.SessionID != "" && l.DB != nil {
		cooldown := int64(0)
		if l.ineffectiveCompactions >= 2 {
			cooldown = time.Now().Unix() + 600
		}
		l.DB.SetCompactionCooldown(l.SessionID, cooldown, l.ineffectiveCompactions)
		l.DB.UpdateSessionSummary(l.SessionID, summary)
	}
}

// trimContext removes old messages when the estimated token count exceeds
// 80% of ContextWindow. Preserves the system message and recent exchanges.
// Reasoning-carrying assistant messages are protected via ProtectReasoningTurns.
// This is a fallback when LLM-powered compaction is unavailable.
func (l *Loop) trimContext() {
	target := l.ContextWindow * 4 / 5
	totalChars := 0
	for _, m := range l.Messages {
		totalChars += len(m.Content) + len(m.ReasoningContent)
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
		removed := len(rest[0].Content) + len(rest[0].ReasoningContent)
		for _, tc := range rest[0].ToolCalls {
			removed += len(tc.Function.Arguments) + len(tc.Function.Name)
		}
		totalChars -= removed
		rest = rest[1:]
	}

	keepStart := len(l.Messages) - len(rest)
	if l.ReasoningProtectTurns > 0 {
		keepStart = ProtectReasoningTurns(l.Messages, keepStart, l.ReasoningProtectTurns)
	}

	newMsgs := make([]types.Message, 0, len(l.Messages)-keepStart+1)
	newMsgs = append(newMsgs, sysMsg)
	newMsgs = append(newMsgs, l.Messages[keepStart:]...)
	l.Messages = newMsgs
	l.resetPruner()
	if l.Pruner != nil {
		l.Pruner.Mark(l.Messages, "post_trim")
	}
}
