package tools

import (
	"context"
	"runtime"
	"testing"
)

func TestBashTool_runsSimpleCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires sh")
	}
	bt := &BashTool{}
	result, err := bt.Execute(context.Background(), `{"command":"echo hello"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result != "hello\n" {
		t.Errorf("result = %q, want %q", result, "hello\n")
	}
}

func TestBashTool_rejectsDangerousCommands(t *testing.T) {
	bt := &BashTool{}
	for _, cmd := range []string{
		`{"command":"rm -rf /"}`,
		`{"command":"shutdown"}`,
		`{"command":"reboot"}`,
		`{"command":"mkfs"}`,
		`{"command":"dd if=/dev/zero"}`,
	} {
		t.Run(cmd, func(t *testing.T) {
			_, err := bt.Execute(context.Background(), cmd)
			if err == nil {
				t.Errorf("expected error for dangerous command: %s", cmd)
			}
		})
	}
}

func TestBashTool_returnsErrorForEmptyCommand(t *testing.T) {
	bt := &BashTool{}
	_, err := bt.Execute(context.Background(), `{"command":""}`)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestBashTool_returnsErrorForInvalidJSON(t *testing.T) {
	bt := &BashTool{}
	_, err := bt.Execute(context.Background(), `not json`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestBashTool_isDangerous(t *testing.T) {
	bt := &BashTool{}
	if !bt.IsDangerous(`{}`) {
		t.Error("BashTool should be dangerous")
	}
}
