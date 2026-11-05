package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/buchenberg/yaah/internal/prompts"
)

// CopyTool duplicates a file inside the workspace, leaving the source in place.
//
// Like move, it exists so a sub-agent without a shell can still do ordinary
// file work: bash is blocked for sub-dispatches by policy and the git tool's
// whitelist excludes cp. Argument decoding and every safety rule (path
// validation, files only, no-clobber unless force) come from prepareTransfer in
// move.go, so the two tools cannot drift apart.
type CopyTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*CopyTool)(nil)
	_ WorkspaceSetter     = (*CopyTool)(nil)
)

func (t *CopyTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *CopyTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *CopyTool) Name() string        { return "copy" }
func (t *CopyTool) Description() string { return prompts.ToolDescription("copy") }

func (t *CopyTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Absolute path of the file to copy."
			},
			"to": {
				"type": "string",
				"description": "Absolute destination path for the copy. Its parent directory must already exist; create it with mkdir first."
			},
			"force": {
				"type": "boolean",
				"description": "Replace the destination when it already exists. Default false: an existing destination is refused.",
				"default": false
			}
		},
		"required": ["path", "to"]
	}`)
}

// IsDangerous is true for every call: a copy writes a file at the destination.
func (t *CopyTool) IsDangerous(string) bool { return true }

func (t *CopyTool) Execute(ctx context.Context, args string) (string, error) {
	req, err := prepareTransfer(ctx, "copy", workspaceOf(t.WS, t.PV), args)
	if err != nil {
		return "", err
	}
	if err := req.ws.Copy(ctx, req.Path, req.To); err != nil {
		return "", fmt.Errorf("copy: %w", err)
	}
	return fmt.Sprintf("Copied %s to %s", req.Path, req.To), nil
}
