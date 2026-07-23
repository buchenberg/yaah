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
	if _, ok := iface.(*TokenDeltaEvent); !ok {
		t.Error("interface must hold *TokenDeltaEvent")
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
	// A nil pointer stored in an interface produces a non-nil interface.
	// This is standard Go behavior: the interface has a type tag even though
	// the underlying pointer is nil.
	var e Event = (*TokenDeltaEvent)(nil)
	if _, ok := e.(*TokenDeltaEvent); !ok {
		t.Error("nil *TokenDeltaEvent must satisfy Event and be type-assertable")
	}

	// But a nil interface value (no type, no value) is truly nil.
	var e2 Event
	if _, ok := e2.(*TokenDeltaEvent); ok {
		t.Error("nil interface must not satisfy type assertion")
	}
}
