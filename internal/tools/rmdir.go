package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path"

	"github.com/buchenberg/yaah/internal/prompts"
)

// RmdirTool removes a directory from the workspace.
//
// It completes the explicit directory pair with mkdir. The default is
// empty-only, like the rmdir command; recursive=true removes the tree, the one
// call where being explicit about the scope matters. A sandboxed sub-agent has
// no shell to fall back on, which is why the leaf tool exists at all.
type RmdirTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*RmdirTool)(nil)
	_ WorkspaceSetter     = (*RmdirTool)(nil)
)

func (t *RmdirTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *RmdirTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *RmdirTool) Name() string        { return "rmdir" }
func (t *RmdirTool) Description() string { return prompts.ToolDescription("rmdir") }

func (t *RmdirTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Absolute path of the directory to remove."
			},
			"recursive": {
				"type": "boolean",
				"description": "Remove the directory and everything under it. Default false: a non-empty directory is refused.",
				"default": false
			}
		},
		"required": ["path"]
	}`)
}

// IsDangerous is true: removing a directory mutates the workspace.
func (t *RmdirTool) IsDangerous(string) bool { return true }

func (t *RmdirTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive"`
	}
	if err := StrictToolArgs("rmdir", t.Schema(), args, &params); err != nil {
		return "", err
	}
	if params.Path == "" {
		return "", fmt.Errorf("rmdir: path is required")
	}

	ws := workspaceOf(t.WS, t.PV)
	resolved, err := ws.ResolveWritePath(params.Path)
	if err != nil {
		return "", fmt.Errorf("rmdir: %w", err)
	}

	// Refuse the workspace root before touching anything: removing it, with or
	// without recursion, would dismantle the session's tree.
	if atWorkspaceRoot(ws, t.PV, resolved) {
		return "", fmt.Errorf("rmdir: refusing to remove the workspace root %s", resolved)
	}

	// Lstat: a symlink is not a directory to remove, and on the host a valid
	// link was already resolved by the path layer. Files get a pointer to
	// delete rather than a generic "not a directory" from the substrate.
	info, err := ws.Lstat(ctx, resolved)
	if err != nil {
		return "", fmt.Errorf("rmdir: %s: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("rmdir: %s is not a directory; use delete for a file", resolved)
	}
	if params.Recursive {
		if err := ws.RemoveAll(ctx, resolved); err != nil {
			return "", fmt.Errorf("rmdir: %w", err)
		}
		return fmt.Sprintf("Removed directory tree %s", resolved), nil
	}
	if err := ws.Remove(ctx, resolved); err != nil {
		return "", fmt.Errorf("rmdir: %w", err)
	}
	return fmt.Sprintf("Removed directory %s", resolved), nil
}

// atWorkspaceRoot reports whether resolved names the workspace root itself.
// The validator's root is checked when one is present (a legacy host workspace
// has no WorkDir), and WorkDir covers an injected sandbox.
func atWorkspaceRoot(ws Workspace, pv *PathValidator, resolved string) bool {
	if pv != nil && pv.WorkspaceRoot != "" && sameWorkspacePath(ws, resolved, pv.WorkspaceRoot) {
		return true
	}
	if root := ws.WorkDir(); root != "" && root != "." && sameWorkspacePath(ws, resolved, root) {
		return true
	}
	if ws.Local() {
		return isRootPath(resolved)
	}
	return path.Clean(resolved) == "/"
}

// sameWorkspacePath compares two paths with the workspace's own semantics:
// host paths resolve symlinks and fold case on Windows, sandbox paths are
// lexical POSIX paths.
func sameWorkspacePath(ws Workspace, a, b string) bool {
	if ws.Local() {
		return sameHostPath(a, b)
	}
	return wsPathClean(ws, a) == wsPathClean(ws, b)
}
