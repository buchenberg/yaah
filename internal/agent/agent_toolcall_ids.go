package agent

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/types"
)

// dedupeToolCallIDs returns msg with every tool call id made unique across
// the conversation: an id that already names a call in history, or that
// appears more than once in msg itself, is renamed to a fresh deterministic
// variant (<id>#dup2, <id>#dup3, ...). The second return value lists the
// original ids that were renamed; it is nil when nothing changed.
//
// Why: the Anthropic Messages API requires each tool_use id to carry
// exactly one tool_result block. Providers occasionally emit the same id
// twice in one response (DeepSeek v4 Pro parallel calls) or re-use an id
// from an earlier turn, and yaah's DSML recovery restarts its per-client
// id sequence at zero on resume — a resumed session can regenerate an id
// its persisted history already carries. Executing every entry as-is
// appends two results for one tool_use, and the provider then rejects
// every later request with 400 "each tool_use must have a single result.
// Found multiple tool_result blocks with id: ...".
//
// Renaming at arrival — before dispatch, persistence, and events — keeps
// the pairing consistent everywhere: the persisted assistant message, the
// tool result, and the replayed request all carry the renamed id. Ids are
// opaque pairing keys to the provider, so the rename is invisible to it.
//
// The inputs are never mutated: msg's ToolCalls slice is copied before the
// first rename, and a message without collisions is returned unchanged.
func dedupeToolCallIDs(history []types.Message, msg types.Message) (types.Message, []string) {
	if len(msg.ToolCalls) == 0 {
		return msg, nil
	}

	// Every id the conversation already uses. Tool results mirror their
	// calls, so assistant messages are the complete id set.
	taken := make(map[string]bool, len(history))
	for _, m := range history {
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			taken[tc.ID] = true
		}
	}

	seen := make(map[string]bool, len(msg.ToolCalls))
	var renamed []string
	for i, tc := range msg.ToolCalls {
		if !seen[tc.ID] && !taken[tc.ID] {
			seen[tc.ID] = true
			continue
		}
		if len(renamed) == 0 {
			// Copy-on-write: the caller's slice (and any backing array it
			// shares with result.Message) must not observe the renames.
			tcs := make([]types.ToolCall, len(msg.ToolCalls))
			copy(tcs, msg.ToolCalls)
			msg.ToolCalls = tcs
		}
		msg.ToolCalls[i].ID = freshToolCallID(taken, tc.ID)
		renamed = append(renamed, tc.ID)
	}
	return msg, renamed
}

// freshToolCallID returns an unused id derived from base: base#dup2,
// base#dup3, ... The candidate is registered in taken before it is
// returned, so a batch of duplicates cannot collide with each other or
// with a variant the history already carries.
func freshToolCallID(taken map[string]bool, base string) string {
	for k := 2; ; k++ {
		candidate := fmt.Sprintf("%s#dup%d", base, k)
		if !taken[candidate] {
			taken[candidate] = true
			return candidate
		}
	}
}
