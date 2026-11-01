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

	// KnownTitle mirrors the persisted session title (empty = untitled).
	// Seeded from the store at build time; the first user message that
	// derives a title transitions it — the store's TouchSession adopts
	// the same value on its debounced flush, so the notification may
	// land one beat ahead of the row.
	KnownTitle string
	// OnTitle fires at most once per untitled session, when the title
	// transitions from empty. Callers publish events.TitleSetEvent so
	// clients display live session names.
	OnTitle func(title string, updatedAt int64)
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
	m := p.newMessage(msg, p.msgIdx)
	// Title transition, mirroring the store's one-shot rule (user-role
	// messages only, while the title column is empty). Fires here — during
	// the prompt Run, while the loop's broker exists — even though the
	// store row lands on the debouncer's flush.
	if msg.Role == "user" && p.KnownTitle == "" {
		if title := memory.DeriveSessionTitle(m.Content); title != "" {
			p.KnownTitle = title
			if p.OnTitle != nil {
				p.OnTitle(title, m.Timestamp)
			}
		}
	}
	if err := p.writeMsg(m); err != nil {
		fmt.Fprintf(os.Stderr, "warning: db persist: %v\n", err)
		// Fail-safe for a cursor left behind the store (an interrupted or
		// overlapping run). Retrying the same occupied index would fail for
		// every later message too, silently freezing the transcript, so
		// resync to the persisted tail and retry once.
		next, rerr := p.db.MaxMessageIdx(p.sessionID)
		if rerr != nil || next < p.msgIdx {
			return
		}
		p.msgIdx = next + 1
		m = p.newMessage(msg, p.msgIdx)
		if err := p.writeMsg(m); err != nil {
			fmt.Fprintf(os.Stderr, "warning: db persist after resync: %v\n", err)
			return
		}
	}
	p.msgIdx++
}

// newMessage projects a conversation message onto a store row at idx. Kept
// separate from Persist so the stale-cursor recovery can rebuild the row
// (the deterministic ID and position both depend on the index).
func (p *SessionPersister) newMessage(msg types.Message, idx int) memory.Message {
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
	return memory.Message{
		ID:                 messageID(p.sessionID, idx, msg.Role, content, msg.ReasoningContent, toolName, msg.ToolCallID, toolCallsJSON),
		SessionID:          p.sessionID,
		Idx:                idx,
		Role:               msg.Role,
		Content:            content,
		ReasoningContent:   msg.ReasoningContent,
		ReasoningSignature: msg.ReasoningSignature,
		ToolName:           toolName,
		ToolCallID:         msg.ToolCallID,
		ToolCalls:          toolCallsJSON,
		Timestamp:          time.Now().Unix(),
		TraceID:            p.traceID,
		TurnID:             p.turnID,
	}
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

// SetMsgIdx sets the message index (used when resuming a session or
// starting a run). A snapshot can be left behind the store by an
// interrupted or overlapping run; reconcile upward against the persisted
// tail so this run appends after it instead of colliding on every write.
func (p *SessionPersister) SetMsgIdx(idx int) {
	if p.db != nil {
		if next, err := p.db.MaxMessageIdx(p.sessionID); err == nil && next >= idx {
			idx = next + 1
		}
	}
	p.msgIdx = idx
}

// TruncateFrom deletes persisted rows at idx >= n and resets the cursor
// to n. Used by turn restore so rolled-back messages cannot resurrect
// when the session resumes (review finding B7).
//
// Fail-safe: when the delete fails the cursor is NOT reset. Orphaned
// rows at idx >= n remain, and resetting the cursor onto them would make
// the next append write rows that collide with the stale ones (duplicate
// idx values / resurrection on resume) — corrupting rather than
// degrading the session. Leaving the cursor past the orphans keeps
// persistence consistent; the stale rows stay until a later successful
// truncation or rebase removes them.
func (p *SessionPersister) TruncateFrom(n int) {
	if p.db == nil {
		p.msgIdx = n
		return
	}
	if err := p.db.DeleteMessagesFrom(p.sessionID, n); err != nil {
		fmt.Fprintf(os.Stderr, "warning: truncate persisted messages failed (cursor left at %d): %v\n", p.msgIdx, err)
		return
	}
	p.msgIdx = n
}

// Rebase resynchronizes persistence after the conversation was replaced
// wholesale (compaction): pending debounced writes are flushed, all of
// the session's rows are deleted, and the new baseline is re-persisted
// from idx 0. Without this the cursor pointed past the shortened slice
// and tail persistence silently no-opped (review finding B7). Message
// IDs are deterministic, so re-persisting an unchanged prefix is
// idempotent in principle — the delete keeps positions unambiguous.
func (p *SessionPersister) Rebase(msgs []types.Message) {
	if p.db == nil {
		p.msgIdx = len(msgs)
		return
	}
	p.Flush()
	if err := p.db.DeleteMessagesFrom(p.sessionID, 0); err != nil {
		// Fail-safe: re-persisting over rows the delete failed to remove
		// would create duplicate idx values with conflicting content.
		// Keep the existing rows and cursor instead — the pre-compaction
		// history stays persisted and new messages append past it.
		fmt.Fprintf(os.Stderr, "warning: rebase persisted messages failed (keeping existing rows): %v\n", err)
		return
	}
	p.msgIdx = 0
	for _, m := range msgs {
		p.Persist(m)
	}
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

// rebasePersistence resynchronizes the persisted session after
// compaction replaced the message list wholesale (review finding B7).
// Wired as ContextManager.OnMessagesReplaced.
func (l *Loop) rebasePersistence(msgs []types.Message) {
	if l.Persister == nil {
		return
	}
	l.Persister.Rebase(msgs)
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
