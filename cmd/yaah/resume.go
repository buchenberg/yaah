package yaah

import (
	"encoding/json"
	"fmt"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/types"
)

// restoredSession is what resume recovery yields for a session being attached:
// the transcript, the conversation id its rows are keyed by, the next message
// index, a possibly-replaced system prompt, and the mode the session was left
// in. One struct rather than a widening return list — the fields travel
// together and every caller wants all of them.
type restoredSession struct {
	Messages     []types.Message
	SessionID    string
	MsgIdx       int
	SystemPrompt string
	// Mode is the persisted session mode: "plan", or "" for build. A
	// resumed plan-mode session starts in plan mode — the mode is a
	// selection the user made, so losing it on resume would silently
	// widen the toolset of a session they had narrowed.
	Mode string
}

// restoreSession loads a prior session from the DB (resumeSessionID) or
// prepares a fresh one. The returned struct carries the transcript, the
// conversation id, the next message index, a possibly-replaced system prompt,
// and the persisted mode.
func restoreSession(db *memory.DB, resumeSessionID, systemPrompt string) (restoredSession, error) {
	out := restoredSession{SystemPrompt: systemPrompt}

	if resumeSessionID == "" {
		return out, nil
	}
	if db == nil {
		return restoredSession{}, fmt.Errorf("cannot resume session: no database available (run 'yaah doctor')")
	}
	session, err := db.GetSession(resumeSessionID)
	if err != nil {
		return restoredSession{}, fmt.Errorf("cannot resume session %s: %w", resumeSessionID, err)
	}
	dbMsgs, err := db.GetMessages(resumeSessionID)
	if err != nil {
		return restoredSession{}, fmt.Errorf("cannot resume session %s: %w", resumeSessionID, err)
	}
	if len(dbMsgs) == 0 {
		return restoredSession{}, fmt.Errorf("session %s not found or has no messages", resumeSessionID)
	}
	messages := make([]types.Message, 0, len(dbMsgs)+1)
	if session.SystemPrompt != "" {
		out.SystemPrompt = session.SystemPrompt
	}
	if session.CompactedSummary != "" {
		messages = append(messages, types.SystemMsg(session.CompactedSummary))
	}
	for _, m := range dbMsgs {
		messages = append(messages, restoreMessage(m))
	}
	if len(messages) > 0 {
		last := messages[len(messages)-1]
		if last.Role == "assistant" && len(last.ToolCalls) > 0 {
			messages = append(messages, types.SystemMsg(
				"Previous execution was interrupted. Please continue from where you left off."))
		}
	}
	out.Messages = messages
	out.SessionID = resumeSessionID
	out.MsgIdx = len(dbMsgs)
	out.Mode = persistedMode(db, resumeSessionID)
	return out, nil
}

// persistedMode reads the session's stored mode and normalizes it to a mode
// this binary can actually enforce. Anything unrecognized — a row written by a
// newer yaah, a hand-edited value — falls back to build, the full-toolset
// default, rather than putting the session in a regime the loop cannot honour.
// A read failure is the same fallback: a resume must not fail because a
// selection could not be recovered.
func persistedMode(db *memory.DB, sessionID string) string {
	mode, err := db.SessionMode(sessionID)
	if err != nil {
		return ""
	}
	if !validMode(mode) {
		return ""
	}
	return mode
}

// restoreMessage maps a persisted message row back into a conversation
// message. Every field the provider needs on replay must be carried: the
// reasoning signature in particular, because a thinking block is only
// re-emitted when both its content and its signature are present.
func restoreMessage(m memory.Message) types.Message {
	msg := types.Message{
		Role:               m.Role,
		Content:            m.Content,
		ReasoningContent:   m.ReasoningContent,
		ReasoningSignature: m.ReasoningSignature,
		Name:               m.ToolName,
		ToolCallID:         m.ToolCallID,
	}
	if m.ToolCalls != "" {
		json.Unmarshal([]byte(m.ToolCalls), &msg.ToolCalls)
	}
	return msg
}
