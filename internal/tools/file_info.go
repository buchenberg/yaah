package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// FileInfoTool returns file metadata without reading content.
// Use before write/edit/delete to check existence, size, modtime — avoid
// redundant work when another delegate already created or updated the file.
type FileInfoTool struct{}

func (t *FileInfoTool) Name() string { return "file_info" }
func (t *FileInfoTool) Description() string {
	return "Get file metadata (size, modtime, existence) without reading content. Use to check if a file already exists before writing, or to verify a write succeeded."
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
	params.FilePath = expandHomeDir(params.FilePath)

	info, err := os.Stat(params.FilePath)
	if err != nil {
		if os.IsNotExist(err) {
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
