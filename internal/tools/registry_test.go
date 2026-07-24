package tools

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"
)

func TestRegistry_registersLeafTools(t *testing.T) {
	r := NewRegistry()
	names := r.List()
	expected := []string{"read", "write", "edit", "delete", "grep", "glob", "ls", "question", "webfetch", "git"}
	if runtime.GOOS == "windows" {
		expected = append(expected, "powershell")
	} else {
		expected = append(expected, "bash")
	}
	for _, name := range expected {
		if r.Get(name) == nil {
			t.Errorf("expected tool %q in registry", name)
		}
	}
	if len(names) < len(expected) {
		t.Errorf("registry has %d tools, expected at least %d", len(names), len(expected))
	}
}

func TestRegistry_emptyRegistry(t *testing.T) {
	r := NewEmptyRegistry()
	if len(r.List()) != 0 {
		t.Errorf("empty registry should have 0 tools, got %d", len(r.List()))
	}
}

func TestRegistry_registerAndGet(t *testing.T) {
	r := NewEmptyRegistry()
	r.Register(&ReadTool{})
	if r.Get("read") == nil {
		t.Error("expected to get 'read' tool after register")
	}
}

func TestRegistry_executeUnknownTool(t *testing.T) {
	r := NewEmptyRegistry()
	_, err := r.Execute(context.Background(), "nonexistent", `{}`)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

func TestNewLeafTool_returnsInstance(t *testing.T) {
	tool := NewLeafTool("read")
	if tool == nil {
		t.Fatal("expected non-nil tool for 'read'")
	}
	if tool.Name() != "read" {
		t.Errorf("expected name 'read', got %q", tool.Name())
	}
}

func TestNewLeafTool_returnsNilForUnknown(t *testing.T) {
	if NewLeafTool("nonexistent") != nil {
		t.Error("expected nil for unknown tool name")
	}
}

func TestDangerClassifier_interfaceSatisfied(t *testing.T) {
	// Tools that should implement DangerClassifier
	var dc DangerClassifier
	dc = &BashTool{}
	if !dc.IsDangerous(`{}`) {
		t.Error("BashTool should be dangerous")
	}
	dc = &WriteTool{}
	if !dc.IsDangerous(`{}`) {
		t.Error("WriteTool should be dangerous")
	}
	dc = &EditTool{}
	if !dc.IsDangerous(`{}`) {
		t.Error("EditTool should be dangerous")
	}
	dc = &DeleteTool{}
	if !dc.IsDangerous(`{}`) {
		t.Error("DeleteTool should be dangerous")
	}
	dc = &PowerShellTool{}
	if !dc.IsDangerous(`{}`) {
		t.Error("PowerShellTool should be dangerous")
	}
}

func TestDangerClassifier_notSatisfiedByReadTool(t *testing.T) {
	rt := &ReadTool{}
	if _, ok := any(rt).(DangerClassifier); ok {
		t.Error("ReadTool should not implement DangerClassifier")
	}
}

func TestToolSchema_validJSON(t *testing.T) {
	tools := []Tool{
		&ReadTool{}, &WriteTool{}, &EditTool{}, &DeleteTool{},
		&GlobTool{}, &GrepTool{}, &LsTool{},
		&BashTool{}, &PowerShellTool{},
		&GitTool{}, &WebFetchTool{},
	}
	for _, tool := range tools {
		t.Run(tool.Name(), func(t *testing.T) {
			var v any
			if err := json.Unmarshal(tool.Schema(), &v); err != nil {
				t.Errorf("schema for %s is not valid JSON: %v", tool.Name(), err)
			}
		})
	}
}

func TestToolDescription_nonEmpty(t *testing.T) {
	tools := []Tool{
		&ReadTool{}, &WriteTool{}, &EditTool{}, &DeleteTool{},
		&GlobTool{}, &GrepTool{}, &LsTool{},
		&BashTool{}, &PowerShellTool{},
		&GitTool{}, &WebFetchTool{},
	}
	for _, tool := range tools {
		t.Run(tool.Name(), func(t *testing.T) {
			if tool.Description() == "" {
				t.Errorf("description for %s is empty", tool.Name())
			}
		})
	}
}
