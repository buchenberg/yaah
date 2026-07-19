package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ansiRegex matches ANSI escape sequences: CSI, OSC, and other VT sequences.
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][AB012]|\x1b[>=<]|\x1b[NOClL]`)

// StripANSI removes ANSI escape sequences from text, leaving plain content.
func StripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

// SnapshotFunc returns the current rendered terminal output as a string.
// Set by the TUI layer to provide the viewport content.
type SnapshotFunc func() string

// GetTerminalOutputTool returns the current rendered terminal output so the
// agent can see how its messages appear to the user. Useful for iterative
// UI/layout adjustments in dev mode.
type GetTerminalOutputTool struct {
	// Snapshot returns the rendered viewport content. If nil, the tool
	// reports that no snapshot is available.
	Snapshot SnapshotFunc
}

func (t *GetTerminalOutputTool) Name() string { return "get_terminal_output" }
func (t *GetTerminalOutputTool) Description() string {
	return "Returns the current rendered terminal output (chat messages, tool results, and formatting) as plain text. Use this to see how your output appears to the user and verify formatting, alignment, and layout."
}

func (t *GetTerminalOutputTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"ansi": {
				"type": "boolean",
				"description": "If true, include ANSI escape codes in the output (colors, styles). Default false (plain text)."
			}
		}
	}`)
}

func (t *GetTerminalOutputTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		ANSI bool `json:"ansi"`
	}
	if args != "" {
		if err := json.Unmarshal([]byte(args), &params); err != nil {
			return "", fmt.Errorf("get_terminal_output: invalid arguments: %w", err)
		}
	}

	if t.Snapshot == nil {
		return "No terminal output available. The snapshot provider is not connected.", nil
	}

	raw := t.Snapshot()
	if raw == "" {
		return "Terminal output is empty.", nil
	}

	if params.ANSI {
		return raw, nil
	}

	cleaned := StripANSI(raw)
	// Collapse runs of 3+ blank lines to 2 for readability.
	cleaned = regexp.MustCompile(`\n{3,}`).ReplaceAllString(cleaned, "\n\n")
	cleaned = strings.TrimSpace(cleaned)
	return cleaned, nil
}
