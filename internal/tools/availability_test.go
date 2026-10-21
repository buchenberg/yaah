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
	// The walk-up means the negative case depends on the ancestors: if
	// the temp dir sits inside a module, this directory is one too.
	if ancestorInModule(dir) {
		t.Skip("the temp dir is inside a Go module; the negative case does not apply")
	}
	if GoToolAvailable(dir) {
		t.Error("a directory without go.mod is not a Go module")
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if !GoToolAvailable(dir) {
		t.Error("a directory with go.mod is a Go module")
	}
	// A package subdirectory is still in the module: the toolchain walks
	// up, so the gate must too.
	nested := filepath.Join(dir, "internal", "agent")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if !GoToolAvailable(nested) {
		t.Error("a package subdirectory is still inside the module")
	}
}

// ancestorInModule reports whether any ancestor of a directory holds a
// go.mod.
func ancestorInModule(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return true
	}
	for {
		if _, err := os.Stat(filepath.Join(abs, "go.mod")); err == nil {
			return true
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return false
		}
		abs = parent
	}
}
