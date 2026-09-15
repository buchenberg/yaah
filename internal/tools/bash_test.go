package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestShellTool_RunsInValidatorWorkDir pins the change that makes isolated
// sub-agents work: the shell tool executes in the validator's WorkDir rather
// than the process cwd.
func TestShellTool_RunsInValidatorWorkDir(t *testing.T) {
	dir := t.TempDir()
	pv := &PathValidator{WorkDir: dir}

	var (
		result string
		err    error
	)
	if runtime.GOOS == "windows" {
		result, err = (&PowerShellTool{PV: pv}).Execute(context.Background(), `{"command":"(Get-Location).Path"}`)
	} else {
		result, err = (&BashTool{PV: pv}).Execute(context.Background(), `{"command":"pwd"}`)
	}
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got := strings.TrimSpace(result)
	gotReal, errA := filepath.EvalSymlinks(got)
	wantReal, errB := filepath.EvalSymlinks(dir)
	if errA == nil && errB == nil {
		if !strings.EqualFold(gotReal, wantReal) {
			t.Errorf("shell cwd = %q, want %q", gotReal, wantReal)
		}
		return
	}
	if !strings.EqualFold(filepath.Clean(got), filepath.Clean(dir)) {
		t.Errorf("shell cwd = %q, want %q", got, dir)
	}
}

// TestShellTool_NoWorkDirUsesProcessCwd pins the default: with no WorkDir the
// shell runs in the process working directory, preserving existing behaviour.
func TestShellTool_NoWorkDirUsesProcessCwd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires sh")
	}
	bt := &BashTool{}
	result, err := bt.Execute(context.Background(), `{"command":"pwd"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Skipf("cannot determine cwd: %v", err)
	}
	gotReal, errA := filepath.EvalSymlinks(strings.TrimSpace(result))
	wantReal, errB := filepath.EvalSymlinks(wd)
	if errA == nil && errB == nil && gotReal != wantReal {
		t.Errorf("shell cwd = %q, want process cwd %q", gotReal, wantReal)
	}
}

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
