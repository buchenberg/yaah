package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/process"
)

// TestBackgroundProcessStartFailureIsError pins yaah-gvv: a launch failure is
// a failed tool call, not a successful one carrying prose.
func TestBackgroundProcessStartFailureIsError(t *testing.T) {
	// An empty PATH makes the shell wrapper itself unresolvable, so Start
	// fails before any command can run.
	t.Setenv("PATH", "")

	tool := &BackgroundProcessTool{Manager: process.NewManager()}
	result, err := tool.Execute(context.Background(), `{"action":"start","command":"echo hi"}`)
	if err == nil {
		t.Fatalf("start failure returned a nil error (result %q)", result)
	}
	if !strings.Contains(err.Error(), "start") || !strings.Contains(err.Error(), "echo hi") {
		t.Errorf("error should name the action and command: %v", err)
	}
}
