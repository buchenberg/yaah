package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
)

// GoToolchainTools are the leaf tools that only make sense inside a Go
// module: they drive the Go toolchain (or a Go package listing) against
// the workspace. A workspace that is not a Go module cannot use them, so
// shipping their schemas to it is pure per-call cost.
var GoToolchainTools = []string{
	"go_test",
	"go_mod",
	"go_outline",
	"go_refactor",
	"staticcheck",
}

// IsGoToolchainTool reports whether a tool is Go toolchain machinery.
func IsGoToolchainTool(name string) bool {
	return slices.Contains(GoToolchainTools, name)
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

// GoToolAvailable reports whether a directory is a Go module. The Go
// toolchain tools are gated on it: a Python (or any non-Go) workspace
// cannot use them.
func GoToolAvailable(root string) bool {
	if root == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(root, "go.mod"))
	return err == nil
}
