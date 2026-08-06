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
type PatchTool struct{ PV *PathValidator }

var _ PathValidatorSetter = (*PatchTool)(nil)

func (t *PatchTool) SetPathValidator(pv *PathValidator) { t.PV = pv }

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
	resolved, err := resolvePathWithPV(t.PV, target)
	if err != nil {
		return "", err
	}
	target = resolved

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
			switch {
			case strings.HasPrefix(pl, "-"):
				h.ops = append(h.ops, hunkOp{kind: '-', text: pl[1:]})
			case strings.HasPrefix(pl, "+"):
				h.ops = append(h.ops, hunkOp{kind: '+', text: pl[1:]})
			case strings.HasPrefix(pl, " "):
				h.ops = append(h.ops, hunkOp{kind: ' ', text: pl[1:]})
			}
		}
	}
	if h != nil {
		hunks = append(hunks, *h)
	}

	if len(hunks) == 0 {
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
		applied := false
		var newlines []string
		i := 0
		for i < len(lines) {
			if matchHunk(lines, i, hk) {
				for _, op := range hk.ops {
					switch op.kind {
					case ' ':
						newlines = append(newlines, lines[i])
						i++
					case '-':
						i++
					case '+':
						newlines = append(newlines, op.text)
					}
				}
				applied = true
				break
			}
			newlines = append(newlines, lines[i])
			i++
		}
		if !applied {
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

type hunkOp struct {
	kind byte // ' ' context, '-' remove, '+' add
	text string
}

type hunk struct {
	ops []hunkOp
}

// matchHunk checks whether the hunk's context and removal lines match
// the file starting at position start. Addition lines are skipped
// (they don't consume input).
func matchHunk(lines []string, start int, hk hunk) bool {
	pos := start
	for _, op := range hk.ops {
		switch op.kind {
		case ' ', '-':
			if pos >= len(lines) {
				return false
			}
			if lines[pos] != op.text && strings.TrimSpace(lines[pos]) != strings.TrimSpace(op.text) {
				return false
			}
			pos++
		case '+':
			// additions don't consume input
		}
	}
	return true
}

func formatHunk(hk hunk) string {
	var sb strings.Builder
	for _, op := range hk.ops {
		sb.WriteByte(op.kind)
		sb.WriteString(op.text)
		sb.WriteByte('\n')
	}
	return sb.String()
}
