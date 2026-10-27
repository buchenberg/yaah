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
	headerTargets, err := patchTargets(patch)
	if err != nil {
		return "", err
	}
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

// patchTargets returns the distinct target paths named by the patch's
// "+++ " header lines. Hunk bodies are skipped using their declared line
// counts, so a hunk line whose added text begins with "++ " is not mistaken for
// a header. Timestamps after a tab are stripped, /dev/null (a deletion) is
// ignored, and a/b prefixes are removed.
//
// A hunk may over-count its body and swallow the next file's header pair. A
// second file's hunk header then appears while the patch claims to describe one
// file. Same-file hunks never overlap, so an old range that starts before the
// previous hunk ends proves the patch is multi-file (or malformed) even though
// the "+++" header was hidden.
func patchTargets(patch string) ([]string, error) {
	lines := strings.Split(patch, "\n")
	var targets []string
	seen := make(map[string]bool)
	add := func(header, prefix string) {
		t := normalizePatchPath(header, prefix)
		if t == "" || seen[t] {
			return
		}
		seen[t] = true
		targets = append(targets, t)
	}

	// A raw "diff --git" line starts a file section: hunk body lines always
	// carry a ' ', '+', '-' or '\' prefix. Count sections rather than parsing
	// their paths, because git does not quote spaces and whitespace cannot
	// reliably separate "a/my file.txt b/my file.txt".
	gitSections := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			gitSections++
		}
	}
	if gitSections > 1 {
		return nil, fmt.Errorf("patch: multi-file diffs are not supported (found %d git sections); apply each file separately", gitSections)
	}

	prevOldStart, prevOldCount := -1, 0
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "@@") {
			oldStart, oldCount := parseHunkOldRange(line)
			if prevOldStart >= 0 && oldStart < prevOldStart+prevOldCount {
				return nil, fmt.Errorf("patch: hunk at line %d overlaps the previous hunk (which ends at line %d); multi-file diffs are not supported — apply each file separately", oldStart, prevOldStart+prevOldCount)
			}
			prevOldStart, prevOldCount = oldStart, oldCount

			oldLeft, newLeft := parseHunkCounts(line)
			i = hunkBodyEnd(lines, i+1, oldLeft, newLeft) - 1
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			add(line, "b/")
		}
	}
	return targets, nil
}

// hunkBodyEnd returns the index of the first line after the hunk body. A line
// that is not a hunk op (the next "@@", a file header, or EOF) is left for the
// caller. A body that runs out of lines returns len(lines).
func hunkBodyEnd(lines []string, start, oldLeft, newLeft int) int {
	j := start
	for ; j < len(lines) && (oldLeft > 0 || newLeft > 0); j++ {
		body := lines[j]
		if body == "" || strings.HasPrefix(body, "@@") {
			return j
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
			return j
		}
	}
	return j
}

// parseHunkOldRange extracts the old start line and line count from a
// "@@ -a,b +c,d @@" header. A missing count means one line.
func parseHunkOldRange(header string) (start, count int) {
	start, count = 0, 1
	for _, field := range strings.Fields(header) {
		if !strings.HasPrefix(field, "-") || len(field) <= 1 {
			continue
		}
		spec := field[1:]
		if comma := strings.IndexByte(spec, ','); comma >= 0 {
			start, _ = strconv.Atoi(spec[:comma])
			spec = spec[comma+1:]
		} else {
			start, _ = strconv.Atoi(spec)
			spec = "1"
		}
		n, err := strconv.Atoi(spec)
		if err == nil && n >= 0 {
			count = n
		}
		return start, count
	}
	return 0, 0
}

// normalizePatchPath strips a "--- "/"+++ " marker, a tab-separated timestamp,
// and the a/ or b/ prefix. It returns "" for an absent or /dev/null path.
func normalizePatchPath(header, prefix string) string {
	p := strings.TrimSpace(header)
	if strings.HasPrefix(p, "+++ ") || strings.HasPrefix(p, "--- ") {
		p = strings.TrimSpace(p[4:])
	}
	if i := strings.IndexByte(p, '\t'); i >= 0 {
		p = p[:i]
	}
	p = strings.TrimPrefix(p, prefix)
	if p == "" || p == "/dev/null" {
		return ""
	}
	return p
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
