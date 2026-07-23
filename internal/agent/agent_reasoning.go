package agent

import (
	"github.com/buchenberg/yaah/internal/types"
)

// defaultReasoningProtectTurns is the number of recent user-message turns whose
// assistant ReasoningContent is preserved in provider requests. It matches the
// pruner's MinTurns so reasoning and tool-output protection windows align.
const defaultReasoningProtectTurns = 2

// prepareRequestMessages builds the ephemeral message slice sent to the
// provider for a single turn. It chains the request-time transformations that
// must NOT mutate the stored conversation history (l.Messages):
//
//  1. repairOrphans  — drop orphaned tool results, synthesize results for
//     interrupted tool calls (allocates a fresh slice).
//  2. applyPruning   — stub soft-pruned tool results (Tier-0 context reclaim).
//  3. stripOldReasoning — drop accumulated ReasoningContent from turns older
//     than the protect window.
//
// Because repairOrphans always returns a new slice, the stored history is never
// mutated by any of these passes.
func (l *Loop) prepareRequestMessages(messages []types.Message) []types.Message {
	msgs := repairOrphans(messages)
	msgs = l.applyPruning(msgs)
	msgs = stripOldReasoning(msgs, l.ReasoningProtectTurns)
	return msgs
}

// stripOldReasoning returns a copy of messages with ReasoningContent removed
// from assistant messages older than protectTurns user-message turns. The
// original messages are not mutated.
//
// Reasoning content is generated fresh by the model on every turn; re-sending
// accumulated reasoning from old turns bloats each provider request without
// improving continuation quality. Stripping it from the ephemeral request keeps
// the prompt small while the stored history (and DB) retain full reasoning for
// session replay and debugging.
//
// Fast path: returns the input slice unchanged (zero alloc) when there is no
// reasoning content to strip before the cutoff, or when there are fewer than
// protectTurns user turns (nothing old enough to strip).
func stripOldReasoning(messages []types.Message, protectTurns int) []types.Message {
	// Find the cutoff index: walk backwards counting user-message turns. The
	// cutoff is the index of the user message that begins the protectTurns-th
	// turn from the end; everything before it is old enough to strip.
	turns := 0
	cutoff := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			turns++
			if turns >= protectTurns {
				cutoff = i
				break
			}
		}
	}

	// Fast path: nothing before the cutoff carries reasoning content.
	needsStrip := false
	for i := 0; i < cutoff; i++ {
		if messages[i].ReasoningContent != "" {
			needsStrip = true
			break
		}
	}
	if !needsStrip {
		return messages
	}

	out := make([]types.Message, len(messages))
	copy(out, messages)
	for i := 0; i < cutoff; i++ {
		out[i].ReasoningContent = ""
	}
	return out
}
