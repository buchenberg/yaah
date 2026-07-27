package memory

import "github.com/buchenberg/yaah/internal/todo"

// SessionRepository manages conversation sessions.
type SessionRepository interface {
	CreateSession(s Session) error
	GetSession(id string) (Session, error)
	ListSessions(limit int) ([]Session, error)
	EndSession(id string, endedAt int64, tokensIn int, tokensOut int) error
	UpdateSessionSummary(id string, summary string) error
	GetCompactionCooldown(sessionID string) (cooldownUntil int64, ineffective int, err error)
	SetCompactionCooldown(sessionID string, cooldownUntil int64, ineffective int) error
}

// MessageRepository manages the message history within sessions.
type MessageRepository interface {
	AddMessage(m Message) error
	GetMessages(sessionID string) ([]Message, error)
	SearchMessages(query string, limit int) ([]Message, error)
}

// MemoryRepository manages long-term memory entries.
type MemoryRepository interface {
	AddMemory(e Entry) error
	AddMemoryDedup(e Entry) (string, error)
	SearchMemory(query string, limit int, tag ...string) ([]Entry, error)
	GetMemory(id string) (Entry, error)
	DeleteMemory(id string) error
	UpdateMemory(id string, text string) error
	ListMemory(limit int) ([]Entry, error)
}

// TodoRepository manages persistent todo items.
type TodoRepository interface {
	todo.Persister
}

// Ensure DB satisfies all repository interfaces at compile time.
var (
	_ SessionRepository = (*DB)(nil)
	_ MessageRepository = (*DB)(nil)
	_ MemoryRepository  = (*DB)(nil)
)
