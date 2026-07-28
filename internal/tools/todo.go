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
	OnWrite func()
}

func (t *TodoWriteTool) Name() string { return "todowrite" }
func (t *TodoWriteTool) Description() string {
	return "Creates and manages a structured todo list for the current session."
}

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
						"content": {"type": "string", "description": "Todo description"},
						"status": {"type": "string", "enum": ["pending", "in_progress", "completed", "cancelled"], "description": "Todo status"},
						"priority": {"type": "string", "enum": ["high", "medium", "low"], "description": "Priority level (default medium)"}
					},
					"required": ["content", "status"]
				}
			}
		},
		"required": ["todos"]
	}`)
}

type todoParams struct {
	Todos []struct {
		Content  string `json:"content"`
		Status   string `json:"status"`
		Priority string `json:"priority"`
	} `json:"todos"`
}

func parseTodoArgs(args string) (todoParams, error) {
	var params todoParams
	firstErr := json.Unmarshal([]byte(args), &params)
	if firstErr == nil {
		return params, nil
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return params, fmt.Errorf("todowrite: invalid arguments: %w", firstErr)
	}
	todosRaw, ok := raw["todos"]
	if !ok {
		return params, fmt.Errorf("todowrite: invalid arguments: %w", firstErr)
	}
	var todosStr string
	if err := json.Unmarshal(todosRaw, &todosStr); err != nil {
		return params, fmt.Errorf("todowrite: invalid arguments: %w", firstErr)
	}
	if err := json.Unmarshal([]byte(todosStr), &params.Todos); err != nil {
		return params, fmt.Errorf("todowrite: invalid arguments: %w", err)
	}
	return params, nil
}

func (t *TodoWriteTool) Execute(ctx context.Context, args string) (string, error) {
	params, err := parseTodoArgs(args)
	if err != nil {
		return "", err
	}

	items := make([]todo.Item, len(params.Todos))
	for i, p := range params.Todos {
		prio := p.Priority
		if prio == "" {
			prio = "medium"
		}
		items[i] = todo.Item{
			ID:       fmt.Sprintf("td-%d", i+1),
			Content:  p.Content,
			Status:   p.Status,
			Priority: prio,
		}
	}

	t.Store.Replace(items)

	if t.OnWrite != nil {
		t.OnWrite()
	}

	completed := 0
	inProgress := 0
	pending := 0
	for _, item := range items {
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
		len(items), completed, inProgress, pending), nil
}
