package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/types"
)

// SessionPersister writes conversation messages to a SQLite database.
// No-op when DB is nil. Errors are logged to stderr but never returned,
// so the agent loop continues even if the database is unavailable.
type SessionPersister struct {
	db        *memory.DB
	debouncer *memory.DebouncedWriter
	sessionID string
	msgIdx    int

	// Cross-link IDs for the current interaction: traceID joins a message
	// row to its OTel span tree, turnID to the Shepherd turn facts. Both
	// empty when OTel is disabled / before the first Run of a prompt.
	traceID string
	turnID  string
}

// NewSessionPersister creates a SessionPersister. Pass nil for db to
// disable persistence entirely.
func NewSessionPersister(db *memory.DB, debouncer *memory.DebouncedWriter, sessionID string) *SessionPersister {
	return &SessionPersister{
		db:        db,
		debouncer: debouncer,
		sessionID: sessionID,
	}
}

// SetTurnContext records the cross-link IDs stamped onto every message
// persisted until the next call. Called by the loop once per Run.
func (p *SessionPersister) SetTurnContext(traceID, turnID string) {
	p.traceID = traceID
	p.turnID = turnID
}

// Persist writes a single message to the database.
func (p *SessionPersister) Persist(msg types.Message) {
	if p.db == nil {
		return
	}
	content := msg.Content
	if content == "" {
		var parts []string
		for _, tc := range msg.ToolCalls {
			parts = append(parts, fmt.Sprintf("[tool:%s] %s", tc.Function.Name, tc.Function.Arguments))
		}
		content = strings.Join(parts, "\n")
	}
	toolCallsJSON := ""
	if len(msg.ToolCalls) > 0 {
		data, _ := json.Marshal(msg.ToolCalls)
		toolCallsJSON = string(data)
	}
	toolName := ""
	if msg.Role == "tool" {
		toolName = msg.Name
	}
	m := memory.Message{
		ID:               messageID(p.sessionID, p.msgIdx, msg.Role, content, msg.ReasoningContent, toolName, msg.ToolCallID, toolCallsJSON),
		SessionID:        p.sessionID,
		Idx:              p.msgIdx,
		Role:             msg.Role,
		Content:          content,
		ReasoningContent: msg.ReasoningContent,
		ToolName:         toolName,
		ToolCallID:       msg.ToolCallID,
		ToolCalls:        toolCallsJSON,
		Timestamp:        time.Now().Unix(),
		TraceID:          p.traceID,
		TurnID:           p.turnID,
	}
	if err := p.writeMsg(m); err != nil {
		fmt.Fprintf(os.Stderr, "warning: db persist: %v\n", err)
		return
	}
	p.msgIdx++
}

// Flush drains any debounced writes.
func (p *SessionPersister) Flush() {
	if p.debouncer != nil {
		p.debouncer.Flush()
	}
}

// DB returns the underlying database, or nil if persistence is disabled.
func (p *SessionPersister) DB() *memory.DB {
	return p.db
}

// MsgIdx returns the next message index for DB inserts.
func (p *SessionPersister) MsgIdx() int {
	return p.msgIdx
}

// SetMsgIdx sets the message index (used when resuming a session).
func (p *SessionPersister) SetMsgIdx(idx int) {
	p.msgIdx = idx
}

// persistMessage persists a message through the Loop's persister.
// It is a thin convenience method so callers don't need to nil-check
// the persister before calling Persist.
func (l *Loop) persistMessage(msg types.Message) {
	if l.Persister == nil {
		return
	}
	l.Persister.Persist(msg)
}

func (p *SessionPersister) writeMsg(m memory.Message) error {
	if p.debouncer != nil {
		return p.debouncer.Update(context.Background(), m)
	}
	if p.db != nil {
		return p.db.AddMessage(m)
	}
	return nil
}

// messageID derives a stable, deterministic ID from all immutable persisted
// message fields. This makes persistence idempotent: the debounced writer
// coalesces a re-submitted message by ID, and the embedding goroutine targets
// a stable row. Position-keyed (session + idx) so identical content at
// different positions stays distinct; content-keyed (all remaining fields) so
// a changed message at the same position gets a different fingerprint.
func messageID(sessionID string, idx int, role, content, reasoning, toolName, toolCallID, toolCalls string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		sessionID, idx, role, content, reasoning, toolName, toolCallID, toolCalls,
	)))
	return hex.EncodeToString(sum[:])
}

// newTurnID returns a random 128-bit hex ID that identifies one
// prompt-to-answer interaction across OTel spans, persisted messages, and
// Shepherd turn facts.
func newTurnID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("turn-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
