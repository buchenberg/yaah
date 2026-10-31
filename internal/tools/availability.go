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

// GoToolchainLeafTools returns the sorted names of the Go toolchain leaf
// tools — the same list IsGoToolchainTool reads, so a registry curation
// and the per-name predicate can never disagree about which tools a
// non-Go workspace is missing. Callers that drop these from a registry
// use it to name what was left out.
func GoToolchainLeafTools() []string {
	out := slices.Clone(goToolchainTools)
	slices.Sort(out)
	return out
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
