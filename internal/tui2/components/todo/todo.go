// Package todo renders the TODO list in the infopane right side panel.
//
// Updated on CtrlTodos events from the agent loop.
package todo

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/todo"
)

// Item wraps a todo.Item for display formatting.
type Item struct {
	todo.Item
}

// Format returns a tview-tagged string for a single todo item.
func Format(item todo.Item) string {
	check := "☐"
	if item.Status == "completed" {
		check = "☑"
	}
	priority := ""
	switch item.Priority {
	case "high":
		priority = "[red]HIGH[-]  "
	case "medium":
		priority = "[yellow]MED[-]   "
	case "low":
		priority = "[dim]LOW[-]   "
	}

	return fmt.Sprintf("  %s %s%s", check, priority, item.Content)
}

// FormatList returns a formatted tview string for a list of todo items.
func FormatList(items []todo.Item) string {
	if len(items) == 0 {
		return "[dim]  No tasks.[-]"
	}
	var b strings.Builder
	for _, item := range items {
		b.WriteString(Format(item))
		b.WriteString("\n")
	}
	return b.String()
}
