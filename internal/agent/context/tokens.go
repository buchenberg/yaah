// Package context provides pure helper functions for agent context-window
// management: token estimation, payload sizing, turn splitting, pruning,
// chunked compaction, and tool-result truncation. It is a leaf package —
// it must not import internal/agent.
package context

import (
	"math"

	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/types"
)

// DefaultEstimateFactor is the default multiplier applied to char/4 token
// estimates to compensate for provider tokenizer undercounting (especially
// for code and JSON).
const DefaultEstimateFactor = 1.3

// DefaultRawCompactionThreshold is the fraction of ContextWindow at which
// compaction fires based on raw (non-cache-adjusted) prompt tokens. It guards
// against latency degradation in heavily-cached conversations where the
// effective-token trigger never fires. 0.25 fires earlier than hermes's 50%
// threshold.
const DefaultRawCompactionThreshold = 0.25

// MaxPayloadBytes is the serialized request size above which the payload-size
// guard forces compaction regardless of token estimates. Token heuristics
// (chars/4) can undercount code and JSON by 2-4x, so a byte-level check catches
// oversized payloads the token trigger misses. ~1.25MB matches kilocode's
// prompt.ts payload-limit prune threshold.
const MaxPayloadBytes = 1_250_000

// Token-budget clamp for the preserved tail after compaction. The budget is
// 25% of the context window, clamped to [MinPreserveTokens, MaxPreserveTokens]
// so huge windows don't over-preserve and small windows keep a usable floor.
const (
	MinPreserveTokens = 2000
	MaxPreserveTokens = 8000
)

// PruneMessageMaxLen is the threshold above which old messages are pruned
// before being sent to the LLM summarizer during compaction.
const PruneMessageMaxLen = 2000

// MinContextFloor is the minimum trigger threshold for compaction, preventing
// over-aggressive compaction on small-window models.
const MinContextFloor = 64000

// SummaryTemplate returns the structured Markdown prompt sent to the compact
// provider. It is loaded from the embedded prompts package.
func SummaryTemplate() string { return prompts.SummaryTemplate() }

// MessageTokens estimates the token count of a single message using chars/4
// for content, reasoning content, plus tool-call arguments. Applies a 10-token
// floor for role/metadata. ReasoningContent is counted because it is serialized
// in every provider request and contributes to the real prompt size; omitting it
// causes token estimates (and therefore compaction triggers) to undercount.
func MessageTokens(m types.Message) int {
	tokens := len(m.Content)/4 + len(m.ReasoningContent)/4
	for _, tc := range m.ToolCalls {
		tokens += len(tc.Function.Arguments)/4 + len(tc.Function.Name)/4
	}
	if tokens < 10 {
		tokens = 10
	}
	return tokens
}

// PreflightTokens estimates the token count for a request payload (messages +
// tools) with a configurable multiplier to compensate for provider tokenizer
// undercounting (especially for code and JSON). The factor parameter defaults
// to 1.3 (DefaultEstimateFactor) and is configurable via EstimateFactor on the
// Loop. Ported from kilocode overflow.ts:8,71.
func PreflightTokens(messages []types.Message, tools []types.ToolDef, factor float64) int {
	total := 0
	for _, m := range messages {
		total += MessageTokens(m)
	}
	for _, t := range tools {
		total += len(t.Function.Description)/4 + len(t.Function.Parameters)/4 + 10
	}
	if factor <= 0 {
		factor = DefaultEstimateFactor
	}
	return int(math.Ceil(float64(total) * factor))
}

// EstimatePayloadBytes estimates the serialized size of a chat request payload
// (messages plus tool definitions) in bytes. It backs the payload-size guard: a
// byte-level check catches oversized requests that the chars/4 token heuristic
// misses, since that heuristic systematically undercounts code and JSON.
func EstimatePayloadBytes(messages []types.Message, tools []types.ToolDef) int {
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

// LastUserPrompt returns the content of the most recent user message in the
// slice, or "" if none exists. Used by compaction to preserve the active task.
func LastUserPrompt(msgs []types.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && msgs[i].Content != "" {
			return msgs[i].Content
		}
	}
	return ""
}

// CountReasoningMessages counts assistant messages carrying
// reasoning_content. The result is an exact count, not a turn count.
func CountReasoningMessages(msgs []types.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == "assistant" && m.ReasoningContent != "" {
			n++
		}
	}
	return n
}
