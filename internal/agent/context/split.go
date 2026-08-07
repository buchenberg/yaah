package context

import (
	"github.com/buchenberg/yaah/internal/types"
)

// TurnRange identifies a contiguous turn: a user message followed by its
// assistant response and any tool results, ending at the next user message
// (or end of conversation).
type TurnRange struct {
	Start int // index of the user message that starts this turn
	End   int // exclusive: index of the next user message or len(messages)
}

// SplitResult describes a compaction split: messages before KeepStart are
// summarized, messages from KeepStart onward are preserved verbatim.
type SplitResult struct {
	KeepStart int // index into messages where the tail begins
}

// Turns segments messages into turn ranges starting at non-index-0 user
// messages. A "user" message at index 0 is the system prompt, not a turn.
// Messages before the first real user message are not in any turn.
// Ported from kilocode compaction.ts:161-177.
func Turns(messages []types.Message) []TurnRange {
	var result []TurnRange
	for i, m := range messages {
		if m.Role != "user" || i == 0 {
			continue
		}
		result = append(result, TurnRange{Start: i, End: len(messages)})
	}
	for i := 0; i < len(result)-1; i++ {
		result[i].End = result[i+1].Start
	}
	return result
}

// PreserveBudget returns the token budget for the preserved tail: 25% of the
// context window, clamped to [MinPreserveTokens, MaxPreserveTokens]. Ported
// from kilocode compaction.ts:152-158 (preserveRecentBudget).
func PreserveBudget(contextWindow int) int {
	budget := contextWindow / 4 // 25%
	if budget < MinPreserveTokens {
		budget = MinPreserveTokens
	}
	if budget > MaxPreserveTokens {
		budget = MaxPreserveTokens
	}
	return budget
}

// SplitTail finds the split point that keeps the most recent turns within the
// preserve budget, without splitting a tool-call/result pair. Walks backwards
// over turns, accumulating token sizes until the budget is exceeded. If a
// single turn exceeds the remaining budget, walks forward within that turn to
// find the earliest message that fits. Ported from kilocode compaction.ts:179-202.
func SplitTail(messages []types.Message, budget int) SplitResult {
	allTurns := Turns(messages)
	if len(allTurns) == 0 {
		return SplitResult{KeepStart: len(messages)} // nothing to split
	}

	// Walk backwards over the most recent turns.
	total := 0
	keepStart := len(messages)
	for i := len(allTurns) - 1; i >= 0; i-- {
		t := allTurns[i]
		turnTokens := 0
		for j := t.Start; j < t.End; j++ {
			turnTokens += MessageTokens(messages[j])
		}
		if total+turnTokens <= budget {
			total += turnTokens
			keepStart = t.Start
			continue
		}
		// Turn doesn't fit entirely — try to split it.
		if splitAt := SplitTurn(messages, t, budget-total); splitAt >= 0 {
			keepStart = splitAt
		}
		break
	}

	// Never summarize past the first message (system prompt protection).
	if keepStart < 1 {
		keepStart = 1
	}
	return SplitResult{KeepStart: keepStart}
}

// SplitTurn finds the earliest message within a turn that fits within the
// remaining budget. Returns -1 if no split is possible (entire turn is too
// large or the turn has only one message). Ported from kilocode compaction.ts:203-218.
func SplitTurn(messages []types.Message, t TurnRange, budget int) int {
	if budget <= 0 || t.End-t.Start <= 1 {
		return -1
	}
	for start := t.Start + 1; start < t.End; start++ {
		size := 0
		for j := start; j < t.End; j++ {
			size += MessageTokens(messages[j])
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

// TruncateRunes slices s to at most maxLen runes, preserving head and tail
// with an ellipsis marker in between. Operates on rune boundaries to avoid
// corrupting multi-byte UTF-8 characters.
func TruncateRunes(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	headLen := maxLen * 2 / 3
	tailLen := maxLen / 3
	return string(runes[:headLen]) + "\n...[truncated]...\n" + string(runes[len(runes)-tailLen:])
}
