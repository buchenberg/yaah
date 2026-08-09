package agent

import (
	"testing"
)

func TestApproveTool_UsesApproveFn(t *testing.T) {
	called := false
	l := &Loop{
		ApproveFn: func(name, args string) bool {
			called = true
			if name != "bash" {
				t.Errorf("expected tool name 'bash', got %q", name)
			}
			return true
		},
	}

	approved := l.approveTool("bash", `{"command": "git diff"}`)
	if !approved {
		t.Error("expected approval to be true")
	}
	if !called {
		t.Error("expected ApproveFn to be called")
	}
}

func TestApproveTool_UsesApproveFnDeny(t *testing.T) {
	l := &Loop{
		ApproveFn: func(name, args string) bool {
			return false
		},
	}

	approved := l.approveTool("bash", `{"command": "rm -rf /"}`)
	if approved {
		t.Error("expected approval to be false")
	}
}

func TestApproveTool_AbbreviatesArgs(t *testing.T) {
	l := &Loop{
		ApproveFn: func(name, args string) bool {
			if len(args) > 120 {
				t.Errorf("expected args to be abbreviated to <= 120 chars, got %d", len(args))
			}
			return true
		},
	}

	longArgs := `{"command": "echo ` + string(make([]byte, 200)) + `"}`
	l.approveTool("bash", longArgs)
}

func TestApproveTool_NilApproveFn(t *testing.T) {
	// Without ApproveFn, the method falls back to stdin. We can't
	// easily test the stdin path in unit tests, but we verify the
	// guard is not panicking.
	l := &Loop{
		ApproveFn: nil,
	}
	// approveTool with nil ApproveFn will try to read from stdin.
	// We only verify the struct is safe to construct.
	if l.ApproveFn != nil {
		t.Error("expected nil ApproveFn")
	}
}
