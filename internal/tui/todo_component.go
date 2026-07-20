package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"github.com/buchenberg/yaah/internal/todo"
)

// todoStatusIcon returns the status glyph for a todo item.
func todoStatusIcon(status string) string {
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

// todoPriorityLabel returns a fixed-width priority label.
func todoPriorityLabel(priority string) string {
	switch priority {
	case "high":
		return "HIGH"
	case "low":
		return "LOW "
	default:
		return "MED "
	}
}

// TodoTable renders a todo list as a borderless table for embedding
// inside a ToolMessage's bordered output box — the box provides the
// border, the collapse toggle, and the truncation budget. Task content
// is truncated, never wrapped, so rows survive the box's inner-width
// wrapping intact.
type TodoTable struct {
	items []todo.Item
	width int
}

// NewTodoTable creates a todo table component. width should be the
// available content width inside the tool box (m.width - 8).
func NewTodoTable(items []todo.Item, width int) TodoTable {
	return TodoTable{items: items, width: width}
}

// Render returns the table, or "" when there are no todos.
func (t TodoTable) Render() string {
	if len(t.items) == 0 {
		return ""
	}

	taskWidth := max(t.width-24, 16)

	rows := make([][]string, 0, len(t.items))
	for _, it := range t.items {
		rows = append(rows, []string{
			todoStatusIcon(it.Status),
			truncateRunes(it.Content, taskWidth),
			todoPriorityLabel(it.Priority),
		})
	}

	tbl := table.New().
		Width(max(t.width, 24)).
		Border(lipgloss.Border{}).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return paletteTitleStyle
			}
			return listItemStyle
		}).
		Headers("STATUS", "TASK", "PRIORITY").
		Rows(rows...)

	return tbl.String()
}

// truncateRunes shortens s to maxWidth display runes with an ellipsis.
func truncateRunes(s string, maxWidth int) string {
	runes := []rune(s)
	if len(runes) <= maxWidth {
		return s
	}
	if maxWidth < 2 {
		return string(runes[:maxWidth])
	}
	return strings.TrimRight(string(runes[:maxWidth-1]), " ") + "…"
}
