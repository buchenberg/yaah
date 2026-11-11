package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/buchenberg/yaah/internal/prompts"
)

// MkdirTool creates a directory inside the workspace, including any missing
// parents.
//
// It is the explicit counterpart to the copy/move parent rule: those tools
// refuse a destination whose parent does not exist, rather than silently
// creating directory trees of their own, and point the model here. A
// directory is also something a sandboxed sub-agent cannot make with bash when
// the shell class is blocked.
type MkdirTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*MkdirTool)(nil)
	_ WorkspaceSetter     = (*MkdirTool)(nil)
)

func (t *MkdirTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *MkdirTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *MkdirTool) Name() string        { return "mkdir" }
func (t *MkdirTool) Description() string { return prompts.ToolDescription("mkdir") }

func (t *MkdirTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Absolute path of the directory to create. Missing parents are created too."
			}
		},
		"required": ["path"]
	}`)
}

// IsDangerous is true: creating a directory mutates the workspace.
func (t *MkdirTool) IsDangerous(string) bool { return true }

func (t *MkdirTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Path string `json:"path"`
	}
	if err := StrictToolArgs("mkdir", t.Schema(), args, &params); err != nil {
		return "", err
	}
	if params.Path == "" {
		return "", fmt.Errorf("mkdir: path is required")
	}

	ws := workspaceOf(t.WS, t.PV)
	resolved, err := ws.ResolveWritePath(params.Path)
	if err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}

	// Stat before MkdirAll: an existing directory is a no-op, and returning
	// early also avoids the sandbox's MkdirAll, which applies its mode to the
	// final directory even when it already exists.
	info, statErr := ws.Stat(ctx, resolved)
	switch {
	case statErr == nil && info.IsDir():
		return fmt.Sprintf("Directory %s already exists", resolved), nil
	case statErr == nil:
		return "", fmt.Errorf("mkdir: %s already exists and is not a directory", resolved)
	case !errors.Is(statErr, fs.ErrNotExist):
		return "", fmt.Errorf("mkdir: %s: %w", resolved, statErr)
	}

	if err := ws.MkdirAll(ctx, resolved, 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	return fmt.Sprintf("Created directory %s", resolved), nil
}
