// Package todo renders the TODO list in the infopane right side panel.
//
// Updated on CtrlTodos events from the agent loop.
package todo

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/todo"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
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

// Build creates a scrollable, bordered TextView for the todo list.
func Build(items []Item) *tview.TextView {
	internal := make([]todo.Item, len(items))
	for i, it := range items {
		internal[i] = it.Item
	}

	tv := tview.NewTextView().
		SetTextAlign(tview.AlignLeft).
		SetDynamicColors(true).
		SetWordWrap(true)
	tv.SetBorder(true).
		SetBorderColor(tcell.ColorGray).
		SetTitle(" Tasks ")

	tv.SetText(FormatList(internal))
	return tv
}
