package agent

import (
	"testing"
	"time"
)

// TestEventInterfaceSatisfaction — the compile-time assertions in events.go
// already verify this, but these runtime tests provide additional coverage.

func TestTokenDeltaEvent(t *testing.T) {
	e := &TokenDeltaEvent{Text: "hello"}
	if e.Text != "hello" {
		t.Errorf("expected 'hello', got %q", e.Text)
	}
	var iface Event = e
	if iface == nil {
		t.Error("interface must be non-nil")
	}
}

func TestThinkingEvent(t *testing.T) {
	e := &ThinkingEvent{Text: "reasoning..."}
	if e.Text != "reasoning..." {
		t.Errorf("expected 'reasoning...', got %q", e.Text)
	}
}

func TestFlushEvent(t *testing.T) {
	e := &FlushEvent{Content: "buffered output"}
	if e.Content != "buffered output" {
		t.Errorf("expected 'buffered output', got %q", e.Content)
	}
}

func TestToolStartEvent(t *testing.T) {
	e := &ToolStartEvent{Name: "read", Args: "file.txt"}
	if e.Name != "read" {
		t.Errorf("expected 'read', got %q", e.Name)
	}
	if e.Args != "file.txt" {
		t.Errorf("expected 'file.txt', got %q", e.Args)
	}
}

func TestToolEndEvent(t *testing.T) {
	e := &ToolEndEvent{
		Name:     "read",
		Args:     "file.txt",
		Result:   "content here",
		Duration: 150 * time.Millisecond,
		Error:    "",
	}
	if e.Duration != 150*time.Millisecond {
		t.Errorf("expected 150ms, got %v", e.Duration)
	}
	if e.Error != "" {
		t.Errorf("expected no error, got %q", e.Error)
	}
}

func TestToolEndEvent_WithError(t *testing.T) {
	e := &ToolEndEvent{
		Name:  "read",
		Error: "file not found",
	}
	if e.Error != "file not found" {
		t.Errorf("expected error, got %q", e.Error)
	}
}

func TestSubAgentStartEvent(t *testing.T) {
	e := &SubAgentStartEvent{
		Role:   "developer",
		Model:  "deepseek-v4",
		Prompt: "Fix the bug in parser.go",
	}
	if e.Role != "developer" {
		t.Errorf("expected 'developer', got %q", e.Role)
	}
	if e.Prompt != "Fix the bug in parser.go" {
		t.Errorf("expected prompt, got %q", e.Prompt)
	}
}

func TestSubAgentEndEvent(t *testing.T) {
	e := &SubAgentEndEvent{
		Role:     "developer",
		Model:    "deepseek-v4",
		Prompt:   "Fix the bug in parser.go",
		Duration: 5 * time.Second,
		Error:    "",
	}
	if e.Duration != 5*time.Second {
		t.Errorf("expected 5s, got %v", e.Duration)
	}
	if e.Error != "" {
		t.Errorf("expected no error, got %q", e.Error)
	}
}

func TestSubAgentEndEvent_WithError(t *testing.T) {
	e := &SubAgentEndEvent{
		Role:  "developer",
		Error: "timeout",
	}
	if e.Error != "timeout" {
		t.Errorf("expected 'timeout', got %q", e.Error)
	}
}

// TestEventPointerReceivers ensures that Event interface satisfaction works
// with pointer types — nil checks in type switches require pointer receivers.
func TestEventPointerReceivers(t *testing.T) {
	// All events use pointer receivers for eventMarker().
	// Verify that passing a nil pointer still satisfies Event.
	var e Event = (*TokenDeltaEvent)(nil)
	if e != nil {
		// The interface itself is non-nil even though the underlying pointer is nil.
		// This is standard Go interface behavior.
		t.Log("nil pointer stored in interface is non-nil interface")
	}

	// But a nil interface value is different.
	var e2 Event
	if e2 != nil {
		t.Error("uninitialized interface must be nil")
	}
}


