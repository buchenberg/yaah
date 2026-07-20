package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ReadTool reads a file and returns its contents. Offsets and limits are
// applied to the split lines (zero-based for line-local offset, limit caps
// the returned line count).
type ReadTool struct{}

func (t *ReadTool) Name() string { return "read" }
func (t *ReadTool) Description() string {
	return "Reads a file from the local filesystem with optional offset and limit."
}

func (t *ReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Path to the file to read"},
			"offset": {"type": "integer", "description": "Line number to start from (1-based)"},
			"limit": {"type": "integer", "description": "Maximum number of lines to return"}
		},
		"required": ["path"]
	}`)
}

func (t *ReadTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("read: invalid arguments: %w", err)
	}
	if params.Path == "" {
		return "", fmt.Errorf("read: path is required")
	}
	params.Path = expandHomeDir(params.Path)

	data, err := os.ReadFile(params.Path)
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
