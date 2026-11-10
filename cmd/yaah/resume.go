package yaah

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/buchenberg/yaah/internal/config"
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
// index, a possibly-replaced system prompt. One struct rather than a
// widening return list — the fields travel together and every caller wants
// all of them. The mode column is not read anywhere: the persona column is
// the only persisted axis (a pre-persona row resumes as the default build
// persona).
type restoredSession struct {
	Messages     []types.Message
	SessionID    string
	MsgIdx       int
	SystemPrompt string
	// Usage is the persisted token accounting, so the resumed session shows
	// and continues the running totals instead of restarting them at zero.
	Usage restoredUsage
}

// sessionRestoreStore is the slice of the session store restore needs: the
// session row and its messages.
type sessionRestoreStore interface {
	GetSession(id string) (memory.Session, error)
	GetMessages(sessionID string) ([]memory.Message, error)
}

// restoreSession loads a prior session from the DB (resumeSessionID) or
// prepares a fresh one. The returned struct carries the transcript, the
// conversation id, the next message index, and a possibly-replaced system
// prompt. The legacy mode column is NOT read here: the persona restore path
// (resolveSessionPersonaFor → storedPersonaName) migrates a legacy plan-mode
// row onto the plan persona, and the persona column is the only persisted
// axis since part 2.
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
	out.Messages = messages
	out.SessionID = resumeSessionID
	out.MsgIdx = len(dbMsgs)
	return out, nil
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

// storedSessionModel reads a resumed session's persisted model pick
// (parity W4) and validates it against today's config before handing it
// back: the column carries the "provider/model" wire form, and the pick
// is only reapplied when it splits into a provider this config still
// defines and a non-empty model. Everything else warns and reports
// not-ok so the resume keeps the routing chain:
//   - "" (or a read failure): nothing was stored pre-W4 — no pick to
//     reapply, no noise beyond a store failure;
//   - a bare model name without a "/" prefix: a legacy row from before
//     the column carried the wire form — it names no provider, so the
//     pick cannot be reapplied safely (a model served by a provider
//     whose pick we cannot reconstruct would silently cross to the
//     wrong endpoint);
//   - a provider the current config dropped: same fallback, the
//     persona-file routing and config default decide as they always did.
func storedSessionModel(db *memory.DB, sessionID string, cfg *config.Config) (provider, model string, ok bool) {
	if db == nil || sessionID == "" {
		return "", "", false
	}
	stored, err := db.SessionModel(sessionID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("resume: reading the stored model pick failed; falling back to the routing chain",
				"session", sessionID, "err", err)
		}
		return "", "", false
	}
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return "", "", false
	}
	parts := strings.SplitN(stored, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		slog.Warn("resume: sessions.model holds a pre-W4 value without a provider; ignoring",
			"session", sessionID, "stored", stored)
		return "", "", false
	}
	if _, defined := cfg.Providers[parts[0]]; !defined {
		slog.Warn("resume: stored model pick's provider is no longer configured; falling back to the routing chain",
			"session", sessionID, "provider", parts[0])
		return "", "", false
	}
	return parts[0], parts[1], true
}

// wireModelValue renders a resolved provider/model pair in the
// "provider/model" wire form the sessions.model column stores (parity W4).
// An unset model renders "" — storedSessionModel reads an empty column as
// no pick — and an unset provider with a model keeps the bare name,
// matching the pre-W4 column contents rather than inventing a "/model"
// value nothing parses.
func wireModelValue(provider, model string) string {
	switch {
	case model == "":
		return ""
	case provider == "":
		return model
	default:
		return provider + "/" + model
	}
}
