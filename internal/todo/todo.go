package todo

import (
	"fmt"
	"strings"
	"sync"
)

// Item represents a single todo item.
type Item struct {
	ID       string `json:"id"`
	Content  string `json:"content"`
	Status   string `json:"status"`   // pending, in_progress, completed, cancelled
	Priority string `json:"priority"` // high, medium, low
}

// Store is a thread-safe in-memory todo store.
type Store struct {
	mu    sync.RWMutex
	items []Item
	db    Persister
}

// Persister saves and loads todos from persistent storage.
type Persister interface {
	SaveTodos(items []Item) error
	LoadTodos() ([]Item, error)
}

// NewStore creates a new todo store.
func NewStore() *Store {
	return &Store{}
}

// NewStoreWithDB creates a todo store backed by persistent storage.
func NewStoreWithDB(db Persister) *Store {
	return &Store{db: db}
}

// LoadFromDB loads todos from persistent storage.
func (s *Store) LoadFromDB() error {
	if s.db == nil {
		return nil
	}
	items, err := s.db.LoadTodos()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.items = items
	s.mu.Unlock()
	return nil
}

// Replace replaces all todo items and persists if a DB is configured.
func (s *Store) Replace(items []Item) {
	s.mu.Lock()
	s.items = items
	if s.db != nil {
		s.db.SaveTodos(items)
	}
	s.mu.Unlock()
}

// Add adds a new todo item.
func (s *Store) Add(id, content, status, priority string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if priority == "" {
		priority = "medium"
	}
	s.items = append(s.items, Item{ID: id, Content: content, Status: status, Priority: priority})
	if s.db != nil {
		s.db.SaveTodos(s.items)
	}
}

// Update updates the status of a todo item by ID.
func (s *Store) Update(id, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID == id {
			s.items[i].Status = status
			break
		}
	}
	if s.db != nil {
		s.db.SaveTodos(s.items)
	}
}

// UpdateContent updates the content of a todo item by ID.
func (s *Store) UpdateContent(id, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID == id {
			s.items[i].Content = content
			break
		}
	}
	if s.db != nil {
		s.db.SaveTodos(s.items)
	}
}

// List returns a copy of all todo items.
func (s *Store) List() []Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Item, len(s.items))
	copy(out, s.items)
	return out
}

// Format returns a formatted string representation of the todo list.
func (s *Store) Format() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.items) == 0 {
		return ""
	}

	var buf strings.Builder
	for _, item := range s.items {
		icon := statusIcon(item.Status)
		prio := priorityLabel(item.Priority)
		fmt.Fprintf(&buf, "  %s [%s] %s\n", icon, prio, item.Content)
	}
	return buf.String()
}

// statusIcon returns a visual icon for the todo status.
func statusIcon(status string) string {
	switch status {
	case "completed":
		return "✓"
	case "in_progress":
		return "→"
	case "cancelled":
		return "✗"
	default:
		return "○"
	}
}

// priorityLabel returns a short label for the priority level.
func priorityLabel(priority string) string {
	switch priority {
	case "high":
		return "HIGH"
	case "low":
		return "LOW "
	default:
		return "MED "
	}
}
