package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

// PatchTool applies unified diff patches to files.
// Parses standard unified diff format and applies hunks sequentially.
type PatchTool struct{}

func (t *PatchTool) Name() string                     { return "patch" }
func (t *PatchTool) Description() string              { return prompts.ToolDescription("patch") }
func (t *PatchTool) IsDangerous(argsJSON string) bool { return true }

func (t *PatchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"patch": {"type": "string", "description": "Unified diff patch text to apply"},
			"filePath": {"type": "string", "description": "Optional: target file path. If omitted, parsed from the patch headers (--- / +++ lines)."}
		},
		"required": ["patch"]
	}`)
}

func (t *PatchTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Patch    string `json:"patch"`
		FilePath string `json:"filePath"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("patch: invalid arguments: %w", err)
	}
	if params.Patch == "" {
		return "", fmt.Errorf("patch: patch text is required")
	}

	target := params.FilePath
	patch := params.Patch

	// Parse the patch for file path if not explicitly provided.
	if target == "" {
		for _, line := range strings.Split(patch, "\n") {
			if strings.HasPrefix(line, "+++ b/") {
				target = strings.TrimSpace(strings.TrimPrefix(line, "+++ b/"))
				break
			}
		}
	}
	if target == "" {
		return "", fmt.Errorf("patch: could not determine file path from patch headers; provide filePath parameter")
	}
	target = expandHomeDir(target)

	data, err := os.ReadFile(target)
	if err != nil {
		return "", fmt.Errorf("patch: %w", err)
	}
	content := string(data)

	applied, err := applyUnifiedDiff(content, patch)
	if err != nil {
		return "", fmt.Errorf("patch: %w", err)
	}

	if err := os.WriteFile(target, []byte(applied), 0o644); err != nil {
		return "", fmt.Errorf("patch: %w", err)
	}

	return fmt.Sprintf("Patch applied to %s (%d → %d lines)", target, countLines(content), countLines(applied)), nil
}

// applyUnifiedDiff parses a unified diff and applies hunks sequentially.
// Returns the patched content or an error if any hunk fails to apply.
func applyUnifiedDiff(content, patch string) (string, error) {
	lines := strings.Split(content, "\n")
	patchLines := strings.Split(patch, "\n")

	var hunks []hunk
	var h *hunk
	for _, pl := range patchLines {
		if strings.HasPrefix(pl, "@@") {
			if h != nil {
				hunks = append(hunks, *h)
			}
			h = &hunk{}
		} else if h != nil {
			if strings.HasPrefix(pl, "-") {
				h.minus = append(h.minus, pl[1:])
			} else if strings.HasPrefix(pl, "+") {
				h.plus = append(h.plus, pl[1:])
			} else if strings.HasPrefix(pl, " ") {
				h.context = append(h.context, pl[1:])
			}
			// Skip header/comment lines (---, +++, etc.)
		}
	}
	if h != nil {
		hunks = append(hunks, *h)
	}

	if len(hunks) == 0 {
		// No hunks found — maybe the patch is just add/remove lines
		// Try a simpler mode: apply add/remove lines to the entire file.
		var result []string
		for _, pl := range patchLines {
			if strings.HasPrefix(pl, "+") {
				result = append(result, pl[1:])
			} else if !strings.HasPrefix(pl, "-") && !strings.HasPrefix(pl, "@@") && !strings.HasPrefix(pl, "---") && !strings.HasPrefix(pl, "+++") {
				result = append(result, pl)
			}
		}
		return strings.Join(result, "\n"), nil
	}

	for _, hk := range hunks {
		var newlines []string
		i := 0
		for i < len(lines) {
			// Try to match the hunk context at this position.
			if matchHunk(lines, i, hk) {
				newlines = append(newlines, hk.context...)
				newlines = append(newlines, hk.plus...)
				i += len(hk.context) + len(hk.minus)
				break
			}
			newlines = append(newlines, lines[i])
			i++
		}
		if i >= len(lines) && len(newlines) < len(lines)+len(hk.plus) {
			return "", fmt.Errorf("patch: hunk failed to apply near:\n%s", formatHunk(hk))
		}
		for i < len(lines) {
			newlines = append(newlines, lines[i])
			i++
		}
		lines = newlines
	}
	return strings.Join(lines, "\n"), nil
}

type hunk struct {
	context []string
	minus   []string
	plus    []string
}

func matchHunk(lines []string, start int, hk hunk) bool {
	if start+len(hk.context)+len(hk.minus) > len(lines) {
		return false
	}
	for j, c := range hk.context {
		if lines[start+j] != c {
			// Fuzzy: try trimmed comparison for whitespace differences
			if strings.TrimSpace(lines[start+j]) != strings.TrimSpace(c) {
				return false
			}
		}
	}
	for j, m := range hk.minus {
		if lines[start+len(hk.context)+j] != m {
			if strings.TrimSpace(lines[start+len(hk.context)+j]) != strings.TrimSpace(m) {
				return false
			}
		}
	}
	return true
}

func formatHunk(hk hunk) string {
	var sb strings.Builder
	for _, c := range hk.context {
		sb.WriteString(" " + c + "\n")
	}
	for _, m := range hk.minus {
		sb.WriteString("-" + m + "\n")
	}
	for _, p := range hk.plus {
		sb.WriteString("+" + p + "\n")
	}
	return sb.String()
}
