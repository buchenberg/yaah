package todo

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/todo"
)

func TestFormat_Pending(t *testing.T) {
	item := todo.Item{Status: "pending", Content: "write tests"}
	out := Format(item)
	if !strings.Contains(out, "write tests") {
		t.Errorf("should contain content, got %q", out)
	}
	if !strings.Contains(out, "⏳") {
		t.Errorf("pending should show hourglass, got %q", out)
	}
}

func TestFormat_InProgress(t *testing.T) {
	item := todo.Item{Status: "in_progress", Content: "implement feature"}
	out := Format(item)
	if !strings.Contains(out, "🔄") {
		t.Errorf("in_progress should show arrows, got %q", out)
	}
}

func TestFormat_Completed(t *testing.T) {
	item := todo.Item{Status: "completed", Content: "setup project"}
	out := Format(item)
	if !strings.Contains(out, "✅") {
		t.Errorf("completed should show checkmark, got %q", out)
	}
}

func TestFormat_Cancelled(t *testing.T) {
	item := todo.Item{Status: "cancelled", Content: "old task"}
	out := Format(item)
	if !strings.Contains(out, "❌") {
		t.Errorf("cancelled should show cross, got %q", out)
	}
}

func TestFormat_UnknownStatus(t *testing.T) {
	item := todo.Item{Status: "unknown_status", Content: "mystery"}
	out := Format(item)
	if !strings.Contains(out, "☐") {
		t.Errorf("unknown status should default to checkbox, got %q", out)
	}
}

func TestFormat_HighPriority(t *testing.T) {
	item := todo.Item{Status: "pending", Priority: "high", Content: "urgent"}
	out := Format(item)
	if !strings.Contains(out, "🔴") {
		t.Errorf("high priority should show red circle, got %q", out)
	}
}

func TestFormat_MediumPriority(t *testing.T) {
	item := todo.Item{Status: "pending", Priority: "medium", Content: "normal"}
	out := Format(item)
	if !strings.Contains(out, "🟡") {
		t.Errorf("medium priority should show yellow circle, got %q", out)
	}
}

func TestFormat_LowPriority(t *testing.T) {
	item := todo.Item{Status: "pending", Priority: "low", Content: "nice to have"}
	out := Format(item)
	if !strings.Contains(out, "🟢") {
		t.Errorf("low priority should show green circle, got %q", out)
	}
}

func TestFormat_NoPriority(t *testing.T) {
	item := todo.Item{Status: "pending", Content: "no priority"}
	out := Format(item)
	if strings.Contains(out, "🔴") || strings.Contains(out, "🟡") || strings.Contains(out, "🟢") {
		t.Errorf("no priority should not show priority indicator, got %q", out)
	}
}

func TestFormatList_Empty(t *testing.T) {
	out := FormatList(nil)
	if !strings.Contains(out, "No tasks") {
		t.Errorf("empty list should show 'No tasks', got %q", out)
	}
}

func TestFormatList_Single(t *testing.T) {
	items := []todo.Item{{Status: "pending", Content: "one task"}}
	out := FormatList(items)
	if !strings.Contains(out, "one task") {
		t.Errorf("should contain task content, got %q", out)
	}
}

func TestFormatList_Multiple(t *testing.T) {
	items := []todo.Item{
		{Status: "completed", Content: "done"},
		{Status: "in_progress", Content: "working"},
		{Status: "pending", Content: "todo"},
	}
	out := FormatList(items)
	if !strings.Contains(out, "done") {
		t.Error("should contain first item")
	}
	if !strings.Contains(out, "working") {
		t.Error("should contain second item")
	}
	if !strings.Contains(out, "todo") {
		t.Error("should contain third item")
	}
	if strings.Count(out, "\n") < 2 {
		t.Error("should have multiple lines")
	}
}
