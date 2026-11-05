package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/buchenberg/yaah/internal/prompts"
)

// RmdirTool removes an empty directory from the workspace.
//
// It completes the explicit directory pair with mkdir. Removal is empty-only,
// like the rmdir command: deleting a tree would make this a recursive
// destructive primitive, so a caller removes the entries first with delete and
// rmdir. A sandboxed sub-agent has no shell to fall back on, which is why the
// leaf tool exists at all.
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
				"description": "Absolute path of the empty directory to remove."
			}
		},
		"required": ["path"]
	}`)
}

// IsDangerous is true: removing a directory mutates the workspace.
func (t *RmdirTool) IsDangerous(string) bool { return true }

func (t *RmdirTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("rmdir: invalid arguments: %w", err)
	}
	if params.Path == "" {
		return "", fmt.Errorf("rmdir: path is required")
	}

	ws := workspaceOf(t.WS, t.PV)
	resolved, err := ws.ResolvePath(params.Path)
	if err != nil {
		return "", fmt.Errorf("rmdir: %w", err)
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
	if err := ws.Remove(ctx, resolved); err != nil {
		return "", fmt.Errorf("rmdir: %w", err)
	}
	return fmt.Sprintf("Removed directory %s", resolved), nil
}
