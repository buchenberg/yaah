package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

// ReadTool reads a file and returns its contents. Offsets and limits are
// applied to the split lines (zero-based for line-local offset, limit caps
// the returned line count).
type ReadTool struct{}

func (t *ReadTool) Name() string        { return "read" }
func (t *ReadTool) Description() string { return prompts.ToolDescription("read") }

func (t *ReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "The absolute path to the file to read"},
			"offset": {"type": "integer", "description": "Line number to start from (1-based)"},
			"limit": {"type": "integer", "description": "Maximum number of lines to return"}
		},
		"required": ["filePath"]
	}`)
}

func (t *ReadTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		FilePath string `json:"filePath"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("read: invalid arguments: %w", err)
	}
	if params.FilePath == "" {
		return "", fmt.Errorf("read: filePath is required")
	}
	params.FilePath = expandHomeDir(params.FilePath)

	info, err := os.Stat(params.FilePath)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("read: %s is a directory, use the ls tool to list its contents", params.FilePath)
	}

	data, err := os.ReadFile(params.FilePath)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}

	if params.Offset > 0 || params.Limit > 0 {
		lines := strings.Split(string(data), "\n")
		if params.Offset < 1 {
			params.Offset = 1
		}
		start := params.Offset - 1
		if start > len(lines) {
			return "", nil
		}
		end := len(lines)
		if params.Limit > 0 && start+params.Limit < end {
			end = start + params.Limit
		}
		return strings.Join(lines[start:end], "\n"), nil
	}

	return string(data), nil
}
