package agent

import (
	"github.com/buchenberg/yaah/internal/types"
)

func repairOrphans(messages []types.Message) []types.Message {
	callIDs := make(map[string]bool)
	resultIDs := make(map[string]bool)
	for _, m := range messages {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				callIDs[tc.ID] = true
			}
		}
		if m.Role == "tool" && m.ToolCallID != "" {
			resultIDs[m.ToolCallID] = true
		}
	}

	var pendingCalls []types.ToolCall
	out := make([]types.Message, 0, len(messages))
	for _, m := range messages {
		if m.Role == "tool" && m.ToolCallID != "" && !callIDs[m.ToolCallID] {
			continue
		}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				if !resultIDs[tc.ID] {
					pendingCalls = append(pendingCalls, tc)
				}
			}
		}
		out = append(out, m)
	}

	for _, tc := range pendingCalls {
		out = append(out, types.ToolResultMsg(
			tc.ID, tc.Function.Name,
			"[error: tool call was interrupted and did not produce a result. you may retry this call if the result is still needed.]",
		))
	}

	return out
}
