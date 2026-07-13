package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/buchenberg/yaah/internal/todo"
)

// TodoWriteTool allows the agent to manage a todo list during a session.
type TodoWriteTool struct {
	Store   *todo.Store
	OnWrite func() // called when todos are updated (for display refresh)
}

func (t *TodoWriteTool) Name() string        { return "todowrite" }
func (t *TodoWriteTool) Description() string { return "Creates and manages a structured todo list for the current session." }

func (t *TodoWriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"todos": {
				"type": "array",
				"description": "The updated todo list",
				"items": {
					"type": "object",
					"properties": {
						"id": {"type": "string", "description": "Unique todo ID"},
						"content": {"type": "string", "description": "Todo description"},
						"status": {"type": "string", "enum": ["pending", "in_progress", "completed", "cancelled"], "description": "Todo status"}
					},
					"required": ["id", "content", "status"]
				}
			}
		},
		"required": ["todos"]
	}`)
}

func (t *TodoWriteTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Todos []struct {
			ID      string `json:"id"`
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("todowrite: invalid arguments: %w", err)
	}

	// Replace the entire todo list
	store := t.Store
	for _, item := range params.Todos {
		// Check if item exists
		found := false
		for _, existing := range store.List() {
			if existing.ID == item.ID {
				store.Update(item.ID, item.Status)
				store.UpdateContent(item.ID, item.Content)
				found = true
				break
			}
		}
		if !found {
			store.Add(item.ID, item.Content, item.Status)
		}
	}

	// Notify display refresh
	if t.OnWrite != nil {
		t.OnWrite()
	}

	// Return summary
	completed := 0
	inProgress := 0
	pending := 0
	for _, item := range store.List() {
		switch item.Status {
		case "completed":
			completed++
		case "in_progress":
			inProgress++
		case "pending":
			pending++
		}
	}

	return fmt.Sprintf("Updated todo list: %d total, %d completed, %d in progress, %d pending",
		len(store.List()), completed, inProgress, pending), nil
}
