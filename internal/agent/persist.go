package agent

import (
	"context"
	"crypto/rand"
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
		ID:         newMessageID(),
		SessionID:  p.sessionID,
		Idx:        p.msgIdx,
		Role:       msg.Role,
		Content:    content,
		ToolName:   toolName,
		ToolCallID: msg.ToolCallID,
		ToolCalls:  toolCallsJSON,
		Timestamp:  time.Now().Unix(),
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

// MsgIdx returns the next message index for DB inserts.
func (p *SessionPersister) MsgIdx() int {
	return p.msgIdx
}

// SetMsgIdx sets the message index (used when resuming a session).
func (p *SessionPersister) SetMsgIdx(idx int) {
	p.msgIdx = idx
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

func newMessageID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
