package yaah

import (
	"encoding/json"
	"fmt"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/types"
)

// restoreSession loads a prior session from the DB (resumeSessionID) or
// creates a new one. Returns messages, sessionID, msgIdx, and a
// possibly-replaced systemPrompt.
func restoreSession(db *memory.DB, resumeSessionID, systemPrompt string) ([]types.Message, string, int, string, error) {
	var messages []types.Message
	var sessionID string
	var msgIdx int

	if resumeSessionID != "" {
		if db == nil {
			return nil, "", 0, "", fmt.Errorf("cannot resume session: no database available (run 'yaah doctor')")
		}
		restored, err := db.GetSession(resumeSessionID)
		if err != nil {
			return nil, "", 0, "", fmt.Errorf("cannot resume session %s: %w", resumeSessionID, err)
		}
		dbMsgs, err := db.GetMessages(resumeSessionID)
		if err != nil {
			return nil, "", 0, "", fmt.Errorf("cannot resume session %s: %w", resumeSessionID, err)
		}
		if len(dbMsgs) == 0 {
			return nil, "", 0, "", fmt.Errorf("session %s not found or has no messages", resumeSessionID)
		}
		messages = make([]types.Message, 0, len(dbMsgs)+1)
		if restored.SystemPrompt != "" {
			systemPrompt = restored.SystemPrompt
		}
		if restored.CompactedSummary != "" {
			messages = append(messages, types.SystemMsg(restored.CompactedSummary))
		}
		for _, m := range dbMsgs {
			msg := types.Message{
				Role:             m.Role,
				Content:          m.Content,
				ReasoningContent: m.ReasoningContent,
				Name:             m.ToolName,
				ToolCallID:       m.ToolCallID,
			}
			if m.ToolCalls != "" {
				json.Unmarshal([]byte(m.ToolCalls), &msg.ToolCalls)
			}
			messages = append(messages, msg)
		}
		if len(messages) > 0 {
			last := messages[len(messages)-1]
			if last.Role == "assistant" && len(last.ToolCalls) > 0 {
				messages = append(messages, types.SystemMsg(
					"Previous execution was interrupted. Please continue from where you left off."))
			}
		}
		sessionID = resumeSessionID
		msgIdx = len(dbMsgs)
		return messages, sessionID, msgIdx, systemPrompt, nil
	}

	return messages, sessionID, msgIdx, systemPrompt, nil
}
