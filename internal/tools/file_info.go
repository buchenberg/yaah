package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/buchenberg/yaah/internal/prompts"
)

// FileInfoTool returns file metadata without reading content.
// Use before write/edit/delete to check existence, size, modtime — avoid
// redundant work when another delegate already created or updated the file.
type FileInfoTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*FileInfoTool)(nil)
	_ WorkspaceSetter     = (*FileInfoTool)(nil)
)

func (t *FileInfoTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *FileInfoTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *FileInfoTool) Name() string { return "file_info" }
func (t *FileInfoTool) Description() string {
	return prompts.ToolDescription("file_info")
}
func (t *FileInfoTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {
				"type": "string",
				"description": "Absolute path to the file to inspect."
			}
		},
		"required": ["filePath"]
	}`)
}

type fileInfoResult struct {
	Exists      bool   `json:"exists"`
	Size        int64  `json:"size"`
	ModTime     string `json:"modtime"`
	IsDir       bool   `json:"is_dir"`
	Permissions string `json:"permissions"`
	Error       string `json:"error,omitempty"`
}

func (t *FileInfoTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		FilePath string `json:"filePath"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("file_info: invalid args: %w", err)
	}
	if params.FilePath == "" {
		return "", fmt.Errorf("file_info: filePath is required")
	}
	ws := workspaceOf(t.WS, t.PV)
	resolved, err := ws.ResolvePath(params.FilePath)
	if err != nil {
		return "", err
	}
	params.FilePath = resolved

	info, err := ws.Stat(ctx, params.FilePath)
	if err != nil {
		// errors.Is (not os.IsNotExist) so a workspace whose Stat returns a
		// wrapped fs.ErrNotExist — the sandbox does — still yields the clean
		// {"exists":false} result instead of leaking shell text.
		if errors.Is(err, fs.ErrNotExist) {
			result := fileInfoResult{Exists: false}
			b, _ := json.Marshal(result)
			return string(b), nil
		}
		result := fileInfoResult{Exists: false, Error: err.Error()}
		b, _ := json.Marshal(result)
		return string(b), nil
	}

	result := fileInfoResult{
		Exists:      true,
		Size:        info.Size(),
		ModTime:     info.ModTime().Format(time.RFC3339),
		IsDir:       info.IsDir(),
		Permissions: info.Mode().String(),
	}
	b, _ := json.Marshal(result)
	return string(b), nil
}
