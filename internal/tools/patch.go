package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

// PatchTool applies unified diff patches to files.
// Parses standard unified diff format and applies hunks sequentially.
type PatchTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*PatchTool)(nil)
	_ WorkspaceSetter     = (*PatchTool)(nil)
)

func (t *PatchTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *PatchTool) SetWorkspace(ws Workspace)          { t.WS = ws }

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

	// A unified diff carries one target. Reject a multi-file diff rather than
	// silently applying every hunk to the first file mentioned.
	headerTargets := patchTargets(patch)
	if len(headerTargets) > 1 {
		return "", fmt.Errorf("patch: multi-file diffs are not supported (found %d targets: %s); apply each file separately",
			len(headerTargets), strings.Join(headerTargets, ", "))
	}
	if target == "" && len(headerTargets) == 1 {
		target = headerTargets[0]
	}
	if target == "" {
		return "", fmt.Errorf("patch: could not determine file path from patch headers; provide filePath parameter")
	}
	ws := workspaceOf(t.WS, t.PV)
	resolved, err := ws.ResolvePath(target)
	if err != nil {
		return "", err
	}
	target = resolved

	data, err := ws.ReadFile(ctx, target)
	if err != nil {
		return "", fmt.Errorf("patch: %w", err)
	}
	content := string(data)

	applied, report := applyUnifiedDiff(content, patch)
	if report.failedCount() > 0 {
		// The target is left byte-identical: a patch is all-or-nothing.
		_, reportErr := report.Report(fmt.Sprintf("patch: %d hunk(s) failed; %s left unchanged:", report.failedCount(), target))
		return "", reportErr
	}

	if err := ws.WriteFile(ctx, target, []byte(applied), 0o644); err != nil {
		return "", fmt.Errorf("patch: %w", err)
	}

	return fmt.Sprintf("Patch applied to %s (%d → %d lines)", target, countLines(content), countLines(applied)), nil
}

// patchTargets returns the distinct target paths named by the patch's "+++"
// header lines. Hunk bodies are skipped using their declared line counts, so a
// hunk line whose added text begins with "++ " is not mistaken for a header.
// Timestamps after a tab are stripped, and /dev/null (a deletion) is ignored.
func patchTargets(patch string) []string {
	lines := strings.Split(patch, "\n")
	var targets []string
	seen := make(map[string]bool)

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "@@") {
			oldLeft, newLeft := parseHunkCounts(line)
			for j := i + 1; j < len(lines) && (oldLeft > 0 || newLeft > 0); j++ {
				body := lines[j]
				i = j
				if body == "" {
					break
				}
				switch body[0] {
				case ' ':
					oldLeft--
					newLeft--
				case '-':
					oldLeft--
				case '+':
					newLeft--
				case '\\':
					// "\ No newline at end of file" consumes no lines.
				default:
					oldLeft, newLeft = 0, 0
				}
			}
			continue
		}

		if !strings.HasPrefix(line, "+++ ") {
			continue
		}
		t := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
		if i := strings.IndexByte(t, '\t'); i >= 0 {
			t = t[:i]
		}
		t = strings.TrimPrefix(t, "b/")
		if t == "" || t == "/dev/null" || seen[t] {
			continue
		}
		seen[t] = true
		targets = append(targets, t)
	}
	return targets
}

// parseHunkCounts extracts the old/new line counts from a "@@ -a,b +c,d @@"
// header. A missing count means one line, matching unified diff syntax.
func parseHunkCounts(header string) (oldN, newN int) {
	oldN, newN = 1, 1
	for _, field := range strings.Fields(header) {
		switch {
		case strings.HasPrefix(field, "-") && len(field) > 1:
			oldN = hunkCount(field[1:])
		case strings.HasPrefix(field, "+") && len(field) > 1:
			newN = hunkCount(field[1:])
		}
	}
	return oldN, newN
}

func hunkCount(s string) int {
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[i+1:]
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// applyUnifiedDiff parses a unified diff and applies hunks sequentially.
// It returns the patched content and a multiResult recording every hunk that
// failed. The content is only written by the caller when no hunk failed, so a
// failed patch leaves the target byte-identical.
func applyUnifiedDiff(content, patch string) (string, *multiResult) {
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

	report := &multiResult{}
	if len(hunks) == 0 {
		// Never guess: a context diff, a truncated paste or a diffstat has no
		// hunks, and the old "fallback" built the new file out of the patch
		// text, silently replacing the target with the patch itself.
		report.failf("no @@ hunks found; the patch argument must be inline unified diff text (a .patch file path is not read)")
		return content, report
	}

	for i, hk := range hunks {
		applied := false
		var newlines []string
		pos := 0
		for pos < len(lines) {
			if matchHunk(lines, pos, hk) {
				for _, op := range hk.ops {
					switch op.kind {
					case ' ':
						newlines = append(newlines, lines[pos])
						pos++
					case '-':
						pos++
					case '+':
						newlines = append(newlines, op.text)
					}
				}
				applied = true
				break
			}
			newlines = append(newlines, lines[pos])
			pos++
		}
		if !applied {
			report.failf("hunk %d failed to apply near:\n%s", i+1, strings.TrimRight(formatHunk(hk), "\n"))
			continue
		}
		for pos < len(lines) {
			newlines = append(newlines, lines[pos])
			pos++
		}
		lines = newlines
	}

	if report.failedCount() > 0 {
		return content, report
	}
	return strings.Join(lines, "\n"), report
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
