// Package todo provides an in-memory todo list for the agent to track
// tasks during a session. The agent can add, update, and display todos
// using the todowrite tool.
package todo

import (
	"fmt"
	"strings"
	"sync"
)

// Item represents a single todo item.
type Item struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"` // pending, in_progress, completed, cancelled
}

// Store is a thread-safe in-memory todo store.
type Store struct {
	mu    sync.RWMutex
	items []Item
}

// NewStore creates a new empty todo store.
func NewStore() *Store {
	return &Store{}
}

// Add adds a new todo item.
func (s *Store) Add(id, content, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, Item{ID: id, Content: content, Status: status})
}

// Update updates the status of a todo item by ID.
func (s *Store) Update(id, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID == id {
			s.items[i].Status = status
			return
		}
	}
}

// UpdateContent updates the content of a todo item by ID.
func (s *Store) UpdateContent(id, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID == id {
			s.items[i].Content = content
			return
		}
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
		fmt.Fprintf(&buf, "  %s %s\n", icon, item.Content)
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
	default: // pending
		return "○"
	}
}
