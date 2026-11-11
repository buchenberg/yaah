package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/types"
)

// AppendTool appends to a file without reading or rewriting it. It is the
// log/ledger counterpart to write: concurrent sub-agents and background
// processes can add records without a read-modify-write race.
type AppendTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*AppendTool)(nil)
	_ WorkspaceSetter     = (*AppendTool)(nil)
)

func (t *AppendTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *AppendTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *AppendTool) Name() string        { return "append" }
func (t *AppendTool) Description() string { return prompts.ToolDescription("append") }

func (t *AppendTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "The absolute path of the file to append to; it is created when missing."},
			"content": {"type": "string", "description": "The text to append."}
		},
		"required": ["filePath", "content"]
	}`)
}

func (t *AppendTool) IsDangerous(string) bool { return true }

func (t *AppendTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		FilePath string `json:"filePath"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("append: invalid arguments: %w", err)
	}
	if params.FilePath == "" {
		return "", fmt.Errorf("append: filePath is required")
	}
	ws := workspaceOf(t.WS, t.PV)
	resolved, err := ws.ResolveWritePath(params.FilePath)
	if err != nil {
		return "", fmt.Errorf("append: %w", err)
	}
	params.FilePath = resolved

	if _, err := ws.Stat(ctx, params.FilePath); err == nil {
		SetSupervisionMeta(ctx, types.SupervisionKeyFileExisted, true)
	} else if errors.Is(err, fs.ErrNotExist) {
		SetSupervisionMeta(ctx, types.SupervisionKeyFileExisted, false)
	}

	if err := ws.Append(ctx, params.FilePath, []byte(params.Content), 0o644); err != nil {
		return "", fmt.Errorf("append: %w", err)
	}
	return fmt.Sprintf("Appended %d bytes to %s", len(params.Content), params.FilePath), nil
}
