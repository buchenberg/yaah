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

// todoPriorityColor returns a colored style for the priority badge.
func todoPriorityColor(priority string) lipgloss.Style {
	s := lipgloss.NewStyle()
	switch priority {
	case "high":
		return s.Foreground(lipgloss.Color("11")).Bold(true) // yellow/bold
	case "medium":
		return s.Foreground(lipgloss.Color("12")) // blue
	case "low":
		return s.Foreground(lipgloss.Color("10")) // green
	default:
		return s.Foreground(lipgloss.Color("15")) // white
	}
}

// todoStatusStyle returns a row style based on the todo item's status.
func todoStatusStyle(status string) lipgloss.Style {
	s := lipgloss.NewStyle()
	switch status {
	case "completed":
		return s.Faint(true).Foreground(lipgloss.Color("10")) // dimmed green
	case "in_progress":
		return s.Bold(true).Foreground(lipgloss.Color("11")) // yellow/bold
	case "cancelled":
		return s.Faint(true).Strikethrough(true) // dimmed strikethrough
	default: // pending
		return s.Foreground(lipgloss.Color("15")) // white
	}
}

// TodoTable renders a todo list as a bordered table with a bold title,
// status-colored rows, and colored priority badges. Rendered standalone
// (not inside a ToolMessage box), so it carries its own border.
type TodoTable struct {
	items []todo.Item
	width int
}

// NewTodoTable creates a todo table component. width should be the
// available render width (e.g. m.width).
func NewTodoTable(items []todo.Item, width int) TodoTable {
	return TodoTable{items: items, width: width}
}

// Render returns the formatted table with a "📋 Tasks" title and border,
// or "" when there are no items.
func (t TodoTable) Render() string {
	if len(t.items) == 0 {
		return ""
	}

	taskWidth := max(t.width-28, 16)

	rows := make([][]string, 0, len(t.items))
	for _, it := range t.items {
		rows = append(rows, []string{
			todoStatusIcon(it.Status),
			truncateRunes(it.Content, taskWidth),
			todoPriorityLabel(it.Priority),
		})
	}

	tbl := table.New().
		Width(max(t.width, 28)).
		Border(lipgloss.RoundedBorder()).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return titleStyle
			}
			if col == 2 {
				return todoPriorityColor(t.items[row].Priority)
			}
			return todoStatusStyle(t.items[row].Status)
		}).
		Headers("", "TASK", "PRIORITY").
		Rows(rows...)

	return titleStyle.Render("📋 Tasks") + "\n" + tbl.String()
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
