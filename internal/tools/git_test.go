package tools

import (
	"context"
	"os/exec"
	"strings"
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
	_, err := gt.Execute(context.Background(), `{"action":"rebase"}`)
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
		{`{"action":"diff_cached"}`, false},
		{`{"action":"log"}`, false},
		{`{"action":"show"}`, false},
		{`{"action":"branch"}`, false},
		{`{"action":"add"}`, true},
		{`{"action":"commit"}`, true},
		{`{"action":"push"}`, true},
		{`{"action":"pull"}`, true},
		{`{"action":"fetch"}`, false},
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

func TestGitTool_rejectsOutputFlag(t *testing.T) {
	gt := &GitTool{}
	tests := []struct {
		args string
		msg  string
	}{
		{`{"action":"diff","flags":["--output=/tmp/pwned.diff"]}`, "--output= form"},
		{`{"action":"diff","flags":["--output","/tmp/pwned.diff"]}`, "bare --output flag"},
	}
	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			_, err := gt.Execute(context.Background(), tt.args)
			if err == nil {
				t.Fatalf("expected rejection for %s", tt.msg)
			}
			if !strings.Contains(err.Error(), "not in the safe whitelist") {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestGitTool_rejectsFlagInjectionViaPaths(t *testing.T) {
	gt := &GitTool{}
	tests := []struct {
		args string
		msg  string
	}{
		{`{"action":"diff","paths":["--help"]}`, "flag injection via diff paths"},
		{`{"action":"push","paths":["--force"]}`, "flag injection via push paths"},
		{`{"action":"pull","paths":["--rebase"]}`, "flag injection via pull paths"},
		{`{"action":"fetch","paths":["--all"]}`, "flag injection via fetch paths"},
		{`{"action":"add","paths":["-f"]}`, "flag injection via add paths"},
		{`{"action":"status","paths":["--ignored"]}`, "flag injection via status paths"},
		{`{"action":"diff","paths":["legit-file","--help"]}`, "flag injection mixed with valid path"},
	}
	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			_, err := gt.Execute(context.Background(), tt.args)
			if err == nil {
				t.Fatal("expected error for flag injection")
			}
			t.Logf("correctly rejected: %v", err)
		})
	}
}

func TestGitTool_allowsDotSlashDashFilenames(t *testing.T) {
	// validatePaths allows ./-foo since it starts with '.', not '-'.
	// This test just validates the guard logic — it doesn't need git.
	gt := &GitTool{}
	// status with ./-filename should pass validation (status is read-only, safe)
	_, err := gt.Execute(context.Background(), `{"action":"status","paths":["./-foo","./-bar"]}`)
	if err != nil && gitAvailable() {
		// If git is available and we still get an error, it should NOT be
		// the flag-injection error.
		if err.Error() == "git: path \"./-foo\" starts with '-'; use '././-foo' instead to prevent flag injection" {
			t.Fatalf("./-foo was incorrectly flagged: %v", err)
		}
	}
}
