package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

// lsMaxResultLen caps directory listing output.
const lsMaxResultLen = 8192

// LsTool lists directory contents with depth control.
type LsTool struct{}

func (t *LsTool) Name() string { return "ls" }
func (t *LsTool) Description() string {
	return prompts.ToolDescription("ls")
}

func (t *LsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Directory to list (default current directory)"},
			"depth": {"type": "integer", "description": "How many levels deep to recurse (default 1, max 5)"}
		}
	}`)
}

func (t *LsTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Path  string `json:"path"`
		Depth int    `json:"depth"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("ls: invalid arguments: %w", err)
	}
	if params.Path == "" {
		params.Path = "."
	}
	params.Path = expandHomeDir(params.Path)
	if params.Depth <= 0 {
		params.Depth = 1
	}
	if params.Depth > 5 {
		params.Depth = 5
	}

	var buf strings.Builder
	err := listDir(&buf, params.Path, "", params.Depth, 0)
	if err != nil {
		return "", fmt.Errorf("ls: %w", err)
	}

	result := buf.String()
	if result == "" {
		return "(empty directory)", nil
	}
	if len(result) > lsMaxResultLen {
		result = result[:lsMaxResultLen] + "\n...[truncated]..."
	}
	return strings.TrimRight(result, "\n"), nil
}

func listDir(w io.Writer, root, prefix string, maxDepth, currentDepth int) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})

	for i, e := range entries {
		if commonIgnoreDirs[e.Name()] {
			continue
		}
		isLast := i == len(entries)-1
		connector := "├── "
		if isLast {
			connector = "└── "
		}
		nextPrefix := "│   "
		if isLast {
			nextPrefix = "    "
		}

		name := e.Name()
		if e.IsDir() {
			name += "/"
		}

		fmt.Fprintf(w, "%s%s%s\n", prefix, connector, name)

		if e.IsDir() && currentDepth < maxDepth {
			childPath := filepath.Join(root, e.Name())
			listDir(w, childPath, prefix+nextPrefix, maxDepth, currentDepth+1)
		}
	}
	return nil
}
