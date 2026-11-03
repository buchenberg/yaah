package yaah

import (
	"encoding/json"
	"fmt"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/types"
)

// restoredUsage is the persisted running token totals for a session being
// resumed: the monotonic counters the usage flush maintains across runs. They
// seed both the resumed session's visible usage and its flush baselines, so a
// resume neither re-displays zero usage nor lets the first flush overwrite a
// larger already-flushed total with a smaller one. Sub-agent cached tokens are
// not persisted (the schema stores sub_tokens_in/out only), so they resume at
// zero.
type restoredUsage struct {
	TokensIn     int
	TokensOut    int
	TokensCached int
	SubTokensIn  int
	SubTokensOut int
}

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
	// Mode is the persisted session mode: "build", or "plan". The empty
	// string is build's storage form and means the same thing (it is also
	// what an unenforceable persisted value normalizes to). A resumed
	// plan-mode session starts in plan mode — the mode is a selection the
	// user made, so losing it on resume would silently widen the toolset of
	// a session they had narrowed.
	Mode string
	// Usage is the persisted token accounting, so the resumed session shows
	// and continues the running totals instead of restarting them at zero.
	Usage restoredUsage
}

// sessionRestoreStore is the slice of the session store restore needs: the
// session row, its messages, and its persisted mode. A narrow interface so a
// test can fail the mode read alone while the transcript reads succeed — the
// fail-closed path is exactly the one that must be exercised, not the
// pre-existing "cannot resume session" error.
type sessionRestoreStore interface {
	GetSession(id string) (memory.Session, error)
	GetMessages(sessionID string) ([]memory.Message, error)
	SessionMode(id string) (string, error)
}

// restoreSession loads a prior session from the DB (resumeSessionID) or
// prepares a fresh one. The returned struct carries the transcript, the
// conversation id, the next message index, a possibly-replaced system prompt,
// and the persisted mode.
func restoreSession(db sessionRestoreStore, resumeSessionID, systemPrompt string) (restoredSession, error) {
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
	out.Usage = restoredUsage{
		TokensIn:     session.TokensIn,
		TokensOut:    session.TokensOut,
		TokensCached: session.TokensCached,
		SubTokensIn:  session.SubTokensIn,
		SubTokensOut: session.SubTokensOut,
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
	mode, err := persistedMode(db, resumeSessionID)
	if err != nil {
		return restoredSession{}, fmt.Errorf("cannot resume session %s: %w", resumeSessionID, err)
	}
	out.Messages = messages
	out.SessionID = resumeSessionID
	out.MsgIdx = len(dbMsgs)
	out.Mode = mode
	return out, nil
}

// persistedMode reads the session's stored mode and normalizes it to a mode
// this binary can actually enforce. Anything unrecognized — a row written by a
// newer yaah, a hand-edited value — falls back to build, the full-toolset
// default, rather than putting the session in a regime the loop cannot honour.
//
// Only an unenforceable stored VALUE falls back. A read FAILURE is returned:
// normalizing it to build would silently widen a session the user narrowed, so
// the fail-open direction is the one thing this recovery must not do. The
// caller fails the resume loudly instead.
func persistedMode(db sessionRestoreStore, sessionID string) (string, error) {
	mode, err := db.SessionMode(sessionID)
	if err != nil {
		return "", fmt.Errorf("read the persisted mode: %w", err)
	}
	if !validMode(mode) {
		return "", nil
	}
	return mode, nil
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
