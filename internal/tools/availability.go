package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// GoToolchainTools are the leaf tools that only make sense inside a Go
// module: they drive the Go toolchain (or a Go package listing) against
// the workspace. A workspace that is not a Go module cannot use them, so
// shipping their schemas to it is pure per-call cost.
var goToolchainTools = []string{
	"go_test",
	"go_mod",
	"go_outline",
	"go_refactor",
	"staticcheck",
}

// IsGoToolchainTool reports whether a tool is Go toolchain machinery.
func IsGoToolchainTool(name string) bool {
	return slices.Contains(goToolchainTools, name)
}

// ShellToolAvailable reports whether a shell tool can actually run here.
// bash is everywhere but Windows; powershell needs a PowerShell on PATH
// (pwsh on Unix, always present on Windows). A role that names a shell
// the machine does not have must not pay for its schema, and the model
// must be told it is gone rather than find out by calling it.
func ShellToolAvailable(name string) bool {
	switch name {
	case "bash":
		return runtime.GOOS != "windows"
	case "powershell":
		if runtime.GOOS == "windows" {
			return true
		}
		_, err := exec.LookPath("pwsh")
		return err == nil
	}
	return true
}

// GoToolAvailable reports whether a directory is inside a Go module. The
// Go toolchain resolves modules by walking up from the working
// directory, so the walk mirrors it: a dispatch rooted in a package
// subdirectory is still in the module.
func GoToolAvailable(root string) bool {
	if root == "" {
		return false
	}
	dir, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// WorkspaceShellAvailable reports whether a shell tool can run in a
// workspace. The workspace decides what its commands execute with, so a
// shell the workspace does not launch is not available there — a
// container that runs `sh` has no PowerShell, and vice versa. An empty
// workspace shell means the workspace does not constrain it.
func WorkspaceShellAvailable(workspaceShell, name string) bool {
	if workspaceShell == "" {
		return true
	}
	pwsh := strings.Contains(strings.ToLower(workspaceShell), "powershell") ||
		strings.Contains(strings.ToLower(workspaceShell), "pwsh")
	switch name {
	case "bash":
		return !pwsh
	case "powershell":
		return pwsh
	}
	return true
}
