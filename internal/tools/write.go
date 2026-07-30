package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/buchenberg/yaah/internal/prompts"
)

// WriteTool writes content to a file, overwriting if it exists.
type WriteTool struct{}

func (t *WriteTool) Name() string        { return "write" }
func (t *WriteTool) Description() string { return prompts.ToolDescription("write") }

func (t *WriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"content": {"type": "string", "description": "The content to write to the file"},
			"filePath": {"type": "string", "description": "The absolute path to the file to write"}
		},
		"required": ["content", "filePath"]
	}`)
}

func (t *WriteTool) IsDangerous(argsJSON string) bool { return true }

func (t *WriteTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Content  string `json:"content"`
		FilePath string `json:"filePath"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("write: invalid arguments: %w", err)
	}
	if params.FilePath == "" {
		return "", fmt.Errorf("write: filePath is required")
	}
	params.FilePath = expandHomeDir(params.FilePath)

	if err := os.WriteFile(params.FilePath, []byte(params.Content), 0o644); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	return fmt.Sprintf("Wrote %d bytes to %s", len(params.Content), params.FilePath), nil
}
