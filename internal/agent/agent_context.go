package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	agentctx "github.com/buchenberg/yaah/internal/agent/context"
	"github.com/buchenberg/yaah/internal/types"
)

// Re-exports for backward compatibility within the agent package (tests
// reference the historical unexported names).
const (
	defaultEstimateFactor = agentctx.DefaultEstimateFactor
	maxPayloadBytes       = agentctx.MaxPayloadBytes
)

var (
	messageTokens        = agentctx.MessageTokens
	preflightTokens      = agentctx.PreflightTokens
	estimatePayloadBytes = agentctx.EstimatePayloadBytes
)

// EstimatedTokens returns the estimated token count for all messages
// in the Loop's conversation history (l.State.Messages). This is the
// canonical estimate used by lifecycle events and control messages.
// Compacted methods should use cm.estimatedTokens() which reads the
// ContextManager's Messages snapshot.
func (l *Loop) EstimatedTokens() int {
	total := 0
	for _, m := range l.State.Messages {
		total += agentctx.MessageTokens(m)
	}
	return total
}

// prepareRequestMessages builds the ephemeral message slice sent to the
// provider for a single turn. It chains the request-time transformations that
// must NOT mutate the stored conversation history (l.State.Messages):
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
	if src := agentctx.CountReasoningMessages(messages); agentctx.CountReasoningMessages(out) < src {
		slog.Error("reasoning_content lost — compaction or trim pipeline may have dropped reasoning-carrying messages",
			"source_count", agentctx.CountReasoningMessages(messages),
			"output_count", agentctx.CountReasoningMessages(out),
		)
	}

	return out
}

// StripAllReasoning permanently removes ReasoningContent from every assistant
// message in l.State.Messages. Called when a thinking-mode provider returns the
// "reasoning_content must be passed back" 400 error — the session is no longer
// in a valid thinking state and must continue without reasoning.
func (l *Loop) StripAllReasoning() {
	for i := range l.State.Messages {
		if l.State.Messages[i].Role == "assistant" {
			l.State.Messages[i].ReasoningContent = ""
		}
	}
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
	if budget < agentctx.MinPreserveTokens {
		budget = agentctx.MinPreserveTokens
	}
	if budget > agentctx.MaxPreserveTokens {
		budget = agentctx.MaxPreserveTokens
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
	l.ctxMgr().compactContext(ctx, threshold)
}

// ForceCompact runs a single compaction pass on the loop's current messages,
// bypassing cooldowns and threshold guards so the user's explicit :compact
// command always runs. It applies defaults first (without creating a broker
// or forwarder goroutine) and flushes the persister on completion.
//
// ForceCompact must NOT be called concurrently with Run on the same Loop.
// It temporarily sets l.View to nil so applyDefaults does not create a
// broker/forwarder goroutine; callers must use a loop that is not running.
//
// The threshold parameter is forwarded to compactContext; pass 0 for the
// default 25% threshold. Cooldowns and ineffective-compaction counters are
// cleared before compaction so the guard at the top of compactContext does
// not skip the pass.
func (l *Loop) ForceCompact(ctx context.Context, threshold float64) {
	// Temporarily nil out View so applyDefaults does not create a broker
	// and forwarder goroutine — we never call Run(), so teardown would
	// never close them, causing a goroutine leak.
	savedView := l.View
	l.View = nil
	l.applyDefaults()
	l.View = savedView

	// Clear cooldown and ineffective-compaction state so the explicit
	// user request is not skipped by guards designed for automatic
	// in-loop compaction.
	cm := l.ctxMgr()
	cm.State.IneffectiveCompactions = 0
	if cm.SessionID != "" && cm.DB != nil {
		if err := cm.DB.SetCompactionCooldown(cm.SessionID, 0, 0); err != nil {
			slog.Warn("ForceCompact: failed to clear compaction cooldown", "error", err)
		}
	}

	// Force the token estimate high enough to bypass the threshold gate.
	// compactContext checks effectiveTokens >= ContextWindow * threshold;
	// setting LastPromptTokens to ContextWindow guarantees the gate fires.
	// Save and restore so subsequent automatic checks see the real estimate.
	savedLastPromptTokens := cm.State.LastPromptTokens
	cm.State.LastPromptTokens = cm.ContextWindow

	l.compactContext(ctx, threshold)

	// Restore the real token estimate so the next automatic compaction
	// check doesn't see an inflated value and compact without cause.
	cm.State.LastPromptTokens = savedLastPromptTokens

	// Flush any debounced writes from the persister so they are not lost.
	if l.Persister != nil {
		l.Persister.Flush()
	}
}

// trimContext removes old messages when the estimated token count exceeds
// 80% of ContextWindow. Preserves the system message and recent exchanges.
// Reasoning-carrying assistant messages are protected via ProtectReasoningTurns.
// This is a fallback when LLM-powered compaction is unavailable.
func (l *Loop) trimContext() {
	l.ctxMgr().trimContext()
}
