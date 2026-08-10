package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
)

func newTestSubagentTraceTool(t *testing.T) (*SubagentTraceTool, *shepherd.SQLiteTraceStore) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "trace.sqlite")
	store, err := shepherd.NewSQLiteTraceStore(path)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Seed some data
	seedTraceStore(t, store, "sub-developer-parent-1", "ls", "read")
	seedTraceStore(t, store, "sub-reviewer-parent-1", "grep", "read", "git")

	return &SubagentTraceTool{TraceDir: dir}, store
}

func seedTraceStore(t *testing.T, store *shepherd.SQLiteTraceStore, owner string, tools ...string) {
	t.Helper()
	for i, name := range tools {
		intentID := owner + ":tool:" + string(rune('a'+i))
		store.Append(shepherd.TrustedAppendContext, shepherd.AppendBatch{
			AppendIntentID: intentID,
			Groups: []shepherd.AppendGroup{{
				TraceOwnerID: owner,
				FactDrafts: []shepherd.RecordDraft{{
					Mode:      shepherd.Declaration,
					SchemaRef: "yaah.tool." + name + ".v1",
					KindLabel: name,
					Payload:   map[string]any{"tool": name, "args": "{}"},
				}},
			}},
		})
	}
}

func TestSubagentTraceTool_ListShowsSubSessions(t *testing.T) {
	tool, _ := newTestSubagentTraceTool(t)

	result, err := tool.Execute(context.Background(), `{"action":"list"}`)
	if err != nil {
		t.Fatalf("list error: %v", err)
	}

	if !strings.Contains(result, "sub-developer-parent-1") {
		t.Error("list should mention sub-developer session")
	}
	if !strings.Contains(result, "sub-reviewer-parent-1") {
		t.Error("list should mention sub-reviewer session")
	}
}

func TestSubagentTraceTool_Profile(t *testing.T) {
	tool, _ := newTestSubagentTraceTool(t)

	result, err := tool.Execute(context.Background(), `{"action":"profile","session_id":"sub-developer-parent-1"}`)
	if err != nil {
		t.Fatalf("profile error: %v", err)
	}

	if !strings.Contains(result, "sub-developer-parent-1") {
		t.Error("profile should mention session ID")
	}
	if !strings.Contains(result, "ls") {
		t.Error("profile should mention ls tool")
	}
	if !strings.Contains(result, "read") {
		t.Error("profile should mention read tool")
	}
	if !strings.Contains(result, "tool calls") {
		t.Error("profile should include tool count summary")
	}
}

func TestSubagentTraceTool_ProfileUnknownSession(t *testing.T) {
	tool, _ := newTestSubagentTraceTool(t)

	_, err := tool.Execute(context.Background(), `{"action":"profile","session_id":"nonexistent"}`)
	if err == nil {
		t.Error("should return error for unknown session")
	}
}

func TestSubagentTraceTool_ProfileMissingSessionID(t *testing.T) {
	tool := &SubagentTraceTool{TraceDir: t.TempDir()}
	_, err := tool.Execute(context.Background(), `{"action":"profile"}`)
	if err == nil || !strings.Contains(err.Error(), "session_id is required") {
		t.Fatalf("expected 'session_id is required' error, got: %v", err)
	}
}

func TestSubagentTraceTool_RequiresTraceDir(t *testing.T) {
	tool := &SubagentTraceTool{}
	_, err := tool.Execute(context.Background(), `{"action":"list"}`)
	if err == nil || !strings.Contains(err.Error(), "shepherd tracing is not enabled") {
		t.Fatalf("expected 'not enabled' error, got: %v", err)
	}
}

func TestSubagentTraceTool_UnknownAction(t *testing.T) {
	tool := &SubagentTraceTool{TraceDir: t.TempDir()}
	_, err := tool.Execute(context.Background(), `{"action":"invalid"}`)
	if err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("expected 'unknown action' error, got: %v", err)
	}
}

func TestTruncateStr(t *testing.T) {
	tests := []struct {
		in     string
		maxLen int
		want   string
	}{
		{"", 10, ""},
		{"short", 10, "short"},
		{"very long string here", 10, "very lo..."},
	}
	for _, tt := range tests {
		got := truncateStr(tt.in, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncateStr(%q, %d) = %q, want %q", tt.in, tt.maxLen, got, tt.want)
		}
	}
}
