package tools

import (
	"context"
	"testing"
)

func TestPowerShellTool_runsSimpleCommand(t *testing.T) {
	if psExecutable() == "" {
		t.Skip("no PowerShell available")
	}
	pt := &PowerShellTool{}
	result, err := pt.Execute(context.Background(), `{"command":"Write-Output hello"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !contains(result, "hello") {
		t.Errorf("expected 'hello' in result, got %q", result)
	}
}

func TestPowerShellTool_returnsErrorForEmptyCommand(t *testing.T) {
	pt := &PowerShellTool{}
	_, err := pt.Execute(context.Background(), `{"command":""}`)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestPowerShellTool_returnsErrorForInvalidJSON(t *testing.T) {
	pt := &PowerShellTool{}
	_, err := pt.Execute(context.Background(), `bad json`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestPowerShellTool_isDangerous(t *testing.T) {
	pt := &PowerShellTool{}
	if !pt.IsDangerous(`{}`) {
		t.Error("PowerShellTool should be dangerous")
	}
}
