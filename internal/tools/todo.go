package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/todo"
)

// TodoWriteTool allows the agent to manage a todo list during a session.
type TodoWriteTool struct {
	Store   *todo.Store
	OnWrite func()
}

func (t *TodoWriteTool) Name() string { return "todowrite" }
func (t *TodoWriteTool) Description() string {
	return prompts.ToolDescription("todo")
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

// validTodoStatus / validTodoPriority mirror the Schema enums. Execute
// validates them because a status the counters below do not know would be
// stored verbatim and then counted in nothing, producing a summary like
// "3 total, 0 completed, 0 in progress, 0 pending".
func validTodoStatus(s string) bool {
	switch s {
	case "pending", "in_progress", "completed", "cancelled":
		return true
	default:
		return false
	}
}

func validTodoPriority(s string) bool {
	switch s {
	case "high", "medium", "low":
		return true
	default:
		return false
	}
}

func parseTodoArgs(schema json.RawMessage, args string) (todoParams, error) {
	var params todoParams
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	keys, err := SchemaPropertyNames(schema)
	if err != nil {
		return params, err
	}
	raw, err := DecodeStrictObject("todowrite", args, keys...)
	if err != nil {
		return params, err
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return params, fmt.Errorf("todowrite: invalid arguments: %w", err)
	}
	if err := json.Unmarshal(payload, &params); err == nil {
		return params, nil
	}

	// Legacy shape: todos carried as a JSON-encoded string.
	todosRaw, ok := raw["todos"]
	if !ok {
		return params, fmt.Errorf("todowrite: invalid arguments: todos is required")
	}
	var todosStr string
	if err := json.Unmarshal(todosRaw, &todosStr); err != nil {
		return params, fmt.Errorf("todowrite: invalid arguments: %w", err)
	}
	if err := json.Unmarshal([]byte(todosStr), &params.Todos); err != nil {
		return params, fmt.Errorf("todowrite: invalid arguments: %w", err)
	}
	return params, nil
}

func (t *TodoWriteTool) Execute(ctx context.Context, args string) (string, error) {
	params, err := parseTodoArgs(t.Schema(), args)
	if err != nil {
		return "", err
	}

	items := make([]todo.Item, len(params.Todos))
	for i, p := range params.Todos {
		if !validTodoStatus(p.Status) {
			return "", fmt.Errorf("todowrite: todo %d has invalid status %q; valid values: pending, in_progress, completed, cancelled", i+1, p.Status)
		}
		prio := p.Priority
		if prio == "" {
			prio = "medium"
		}
		if !validTodoPriority(prio) {
			return "", fmt.Errorf("todowrite: todo %d has invalid priority %q; valid values: high, medium, low", i+1, prio)
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
