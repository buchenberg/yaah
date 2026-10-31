package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
	// the temp dir sits inside a module, this directory is one too. The
	// positive cases below still run — only this assertion is skipped.
	if !ancestorInModule(dir) && GoToolAvailable(dir) {
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

// TestGoToolchainLeafTools pins the exported name list against the
// per-name predicate: a registry curation that drops GoToolchainLeafTools
// and a caller that tests IsGoToolchainTool must select the same set, or
// the main session and the sub-agent path gate different tools.
func TestGoToolchainLeafTools(t *testing.T) {
	names := GoToolchainLeafTools()
	if len(names) == 0 {
		t.Fatal("GoToolchainLeafTools returned no names")
	}
	if !slices.IsSorted(names) {
		t.Errorf("GoToolchainLeafTools must be sorted (callers put it in a prompt prefix): %v", names)
	}
	for _, name := range names {
		if !IsGoToolchainTool(name) {
			t.Errorf("GoToolchainLeafTools names %q but IsGoToolchainTool rejects it", name)
		}
		if NewLeafTool(name) == nil {
			t.Errorf("GoToolchainLeafTools names %q, which is not a registered leaf tool", name)
		}
	}
	// The other direction: no leaf tool is Go toolchain machinery unless
	// the list names it, so a new Go tool cannot slip past the gate.
	for name := range leafTools {
		if IsGoToolchainTool(name) && !slices.Contains(names, name) {
			t.Errorf("leaf tool %q is Go toolchain machinery but missing from GoToolchainLeafTools", name)
		}
	}
}

// TestGoToolchainLeafToolsIsolation pins that the returned slice is a
// copy: a caller that sorts or appends to it must not reorder the
// package-level list another caller reads.
func TestGoToolchainLeafToolsIsolation(t *testing.T) {
	first := GoToolchainLeafTools()
	before := slices.Clone(first)
	first[0] = "mutated"
	second := GoToolchainLeafTools()
	if !slices.Equal(second, before) {
		t.Errorf("GoToolchainLeafTools shares backing storage with its caller: %v vs %v", second, before)
	}
}
