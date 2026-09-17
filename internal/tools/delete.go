package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/buchenberg/yaah/internal/prompts"
)

// DeleteTool removes a file from the workspace.
type DeleteTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*DeleteTool)(nil)
	_ WorkspaceSetter     = (*DeleteTool)(nil)
)

func (t *DeleteTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *DeleteTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *DeleteTool) Name() string        { return "delete" }
func (t *DeleteTool) Description() string { return prompts.ToolDescription("delete") }

func (t *DeleteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "The absolute path of the file to delete"}
		},
		"required": ["filePath"]
	}`)
}

func (t *DeleteTool) IsDangerous(argsJSON string) bool { return true }

func (t *DeleteTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		FilePath string `json:"filePath"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("delete: invalid arguments: %w", err)
	}
	if params.FilePath == "" {
		return "", fmt.Errorf("delete: filePath is required")
	}
	ws := workspaceOf(t.WS, t.PV)
	resolved, err := ws.ResolvePath(params.FilePath)
	if err != nil {
		return "", err
	}
	params.FilePath = resolved

	if err := ws.Remove(ctx, params.FilePath); err != nil {
		return "", fmt.Errorf("delete: %w", err)
	}
	return fmt.Sprintf("Deleted %s", params.FilePath), nil
}
