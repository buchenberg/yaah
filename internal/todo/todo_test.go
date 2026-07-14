package todo

import (
	"testing"
)

func TestTodoStore_AddAndList(t *testing.T) {
	store := NewStore()

	store.Add("task-1", "Implement feature A", "pending", "high")
	store.Add("task-2", "Write tests", "pending", "medium")

	todos := store.List()
	if len(todos) != 2 {
		t.Fatalf("expected 2 todos, got %d", len(todos))
	}
	if todos[0].Content != "Implement feature A" {
		t.Errorf("first todo content = %q", todos[0].Content)
	}
}

func TestTodoStore_UpdateStatus(t *testing.T) {
	store := NewStore()
	store.Add("task-1", "Implement feature A", "pending", "high")

	store.Update("task-1", "in_progress")
	todos := store.List()
	if todos[0].Status != "in_progress" {
		t.Errorf("status = %q, want in_progress", todos[0].Status)
	}

	store.Update("task-1", "completed")
	todos = store.List()
	if todos[0].Status != "completed" {
		t.Errorf("status = %q, want completed", todos[0].Status)
	}
}

func TestTodoStore_UpdateContent(t *testing.T) {
	store := NewStore()
	store.Add("task-1", "Old content", "pending", "medium")

	store.UpdateContent("task-1", "New content")
	todos := store.List()
	if todos[0].Content != "New content" {
		t.Errorf("content = %q, want New content", todos[0].Content)
	}
}

func TestTodoStore_Format(t *testing.T) {
	store := NewStore()
	store.Add("task-1", "Implement feature A", "completed", "high")
	store.Add("task-2", "Write tests", "in_progress", "medium")
	store.Add("task-3", "Deploy", "pending", "low")

	output := store.Format()
	if len(output) == 0 {
		t.Error("expected non-empty format output")
	}
	if !containsStr(output, "\u2713") {
		t.Error("expected checkmark for completed task")
	}
}

func TestTodoStore_Empty(t *testing.T) {
	store := NewStore()
	todos := store.List()
	if len(todos) != 0 {
		t.Errorf("expected 0 todos, got %d", len(todos))
	}
	if store.Format() != "" {
		t.Error("expected empty format for empty store")
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
