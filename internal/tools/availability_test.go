package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestShellToolAvailable pins the shell gate (yaah-9n0): bash is
// everywhere but Windows, powershell needs a PowerShell on PATH.
func TestShellToolAvailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		if ShellToolAvailable("bash") {
			t.Error("bash is not available on Windows")
		}
		if !ShellToolAvailable("powershell") {
			t.Error("powershell is available on Windows")
		}
	} else {
		if !ShellToolAvailable("bash") {
			t.Error("bash is available on Unix")
		}
		// pwsh may or may not be installed; the gate must agree with
		// what is on PATH rather than with the platform alone.
		if _, err := os.Stat("/usr/bin/pwsh"); err == nil {
			t.Log("pwsh present")
		}
	}
	if !ShellToolAvailable("read") {
		t.Error("a non-shell tool is always available")
	}
}

// TestGoToolAvailable pins the Go-module gate: the Go toolchain tools
// are only useful in a Go module.
func TestGoToolAvailable(t *testing.T) {
	if GoToolAvailable("") {
		t.Error("an empty root is not a Go module")
	}
	dir := t.TempDir()
	if GoToolAvailable(dir) {
		t.Error("a directory without go.mod is not a Go module")
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if !GoToolAvailable(dir) {
		t.Error("a directory with go.mod is a Go module")
	}
}
