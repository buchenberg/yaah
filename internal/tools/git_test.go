package tools

import (
	"context"
	"os/exec"
	"testing"
)

func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func TestGitTool_status(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not on PATH")
	}
	gt := &GitTool{}
	result, err := gt.Execute(context.Background(), `{"action":"status"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	// status --porcelain returns something (possibly empty) without error
	t.Logf("status result: %q", result)
}

func TestGitTool_rejectsUnsupportedAction(t *testing.T) {
	gt := &GitTool{}
	_, err := gt.Execute(context.Background(), `{"action":"push"}`)
	if err == nil {
		t.Fatal("expected error for unsupported action")
	}
}

func TestGitTool_returnsErrorForEmptyAction(t *testing.T) {
	gt := &GitTool{}
	_, err := gt.Execute(context.Background(), `{}`)
	if err == nil {
		t.Fatal("expected error for empty action")
	}
}

func TestGitTool_addRequiresPaths(t *testing.T) {
	gt := &GitTool{}
	_, err := gt.Execute(context.Background(), `{"action":"add"}`)
	if err == nil {
		t.Fatal("expected error for add without paths")
	}
}

func TestGitTool_commitRequiresMessage(t *testing.T) {
	gt := &GitTool{}
	_, err := gt.Execute(context.Background(), `{"action":"commit"}`)
	if err == nil {
		t.Fatal("expected error for commit without message")
	}
}

func TestGitTool_isDangerousForMutatingActions(t *testing.T) {
	gt := &GitTool{}
	tests := []struct {
		args      string
		dangerous bool
	}{
		{`{"action":"status"}`, false},
		{`{"action":"diff"}`, false},
		{`{"action":"diff_staged"}`, false},
		{`{"action":"log"}`, false},
		{`{"action":"show"}`, false},
		{`{"action":"branch"}`, false},
		{`{"action":"add"}`, true},
		{`{"action":"commit"}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.args, func(t *testing.T) {
			if got := gt.IsDangerous(tt.args); got != tt.dangerous {
				t.Errorf("IsDangerous(%s) = %v, want %v", tt.args, got, tt.dangerous)
			}
		})
	}
}

func TestGitTool_isNotDangerousForInvalidJSON(t *testing.T) {
	gt := &GitTool{}
	if gt.IsDangerous(`bad json`) {
		t.Error("expected false for invalid JSON")
	}
}
