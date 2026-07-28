package tools

import (
	"context"
	"testing"

	"github.com/buchenberg/yaah/internal/todo"
)

func TestTodoWriteTool_Execute(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		wantErr bool
		want    string
	}{
		{
			name: "normal array",
			args: `{"todos":[{"content":"task 1","status":"pending","priority":"high"}]}`,
			want: "Updated todo list: 1 total, 0 completed, 0 in progress, 1 pending",
		},
		{
			name: "string-encoded array",
			args: `{"todos":"[{\"content\":\"task 1\",\"status\":\"completed\",\"priority\":\"low\"}]"}`,
			want: "Updated todo list: 1 total, 1 completed, 0 in progress, 0 pending",
		},
		{
			name: "string-encoded array multiple items",
			args: `{"todos":"[{\"content\":\"a\",\"status\":\"pending\"},{\"content\":\"b\",\"status\":\"in_progress\"}]"}`,
			want: "Updated todo list: 2 total, 0 completed, 1 in progress, 1 pending",
		},
		{
			name: "empty object gives empty list",
			args: `{}`,
			want: "Updated todo list: 0 total, 0 completed, 0 in progress, 0 pending",
		},
		{
			name:    "invalid json",
			args:    `not json`,
			wantErr: true,
		},
		{
			name:    "todos is number",
			args:    `{"todos": 42}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := &TodoWriteTool{Store: todo.NewStore()}
			got, err := tool.Execute(context.Background(), tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
