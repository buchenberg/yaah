package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

// editEntry is a single edit operation within a multi-edit call.
type editEntry struct {
	OldString string `json:"oldString"`
	NewString string `json:"newString"`
}

// EditTool performs exact string replacements in a file, with fuzzy fallback
// when exact match fails. Supports multi-edit via an edits[] array.
type EditTool struct{}

func (t *EditTool) Name() string        { return "edit" }
func (t *EditTool) Description() string { return prompts.ToolDescription("edit") }

func (t *EditTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "The absolute path to the file to edit"},
			"oldString": {"type": "string", "description": "The text to replace"},
			"newString": {"type": "string", "description": "The text to replace with (must differ from oldString)"},
			"replaceAll": {"type": "boolean", "description": "Replace all occurrences (default false)"},
			"edits": {"type": "array", "description": "Array of {oldString, newString} for batch edits", "items": {"type": "object", "properties": {"oldString": {"type": "string"}, "newString": {"type": "string"}}, "required": ["oldString", "newString"]}}
		},
		"required": ["filePath"]
	}`)
}

func (t *EditTool) IsDangerous(argsJSON string) bool { return true }

func (t *EditTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		FilePath   string      `json:"filePath"`
		OldString  string      `json:"oldString"`
		NewString  string      `json:"newString"`
		ReplaceAll bool        `json:"replaceAll"`
		Edits      []editEntry `json:"edits"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("edit: invalid arguments: %w", err)
	}
	if params.FilePath == "" {
		return "", fmt.Errorf("edit: filePath is required")
	}
	params.FilePath = expandHomeDir(params.FilePath)

	if len(params.Edits) > 0 {
		return t.executeMultiEdit(params.FilePath, params.Edits)
	}

	if params.OldString == "" {
		return "", fmt.Errorf("edit: oldString or edits[] is required")
	}
	if params.OldString == params.NewString {
		return "", fmt.Errorf("edit: oldString and newString must differ")
	}

	return t.executeSingleEdit(params.FilePath, params.OldString, params.NewString, params.ReplaceAll)
}

func (t *EditTool) executeSingleEdit(filePath, oldStr, newStr string, replaceAll bool) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}

	content := string(data)
	origLines := countLines(content)
	count := strings.Count(content, oldStr)

	matched := oldStr
	if count == 0 {
		matched, count = tryFuzzyMatch(content, oldStr)
		if count == 0 {
			return "", fmt.Errorf("edit: oldString not found in %s", filePath)
		}
	}

	if !replaceAll {
		if count > 1 {
			return "", fmt.Errorf("edit: found %d matches for oldString in %s; use replaceAll or provide more context", count, filePath)
		}
		content = strings.Replace(content, matched, newStr, 1)
	} else {
		content = strings.ReplaceAll(content, matched, newStr)
	}

	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}

	replaced := count
	if !replaceAll {
		replaced = 1
	}
	newLines := countLines(content)
	return formatEditResult(filePath, replaced, origLines, newLines), nil
}

func (t *EditTool) executeMultiEdit(filePath string, edits []editEntry) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}

	content := string(data)
	origLines := countLines(content)
	totalReplaced := 0
	var failures []string

	for i, e := range edits {
		if e.OldString == e.NewString {
			failures = append(failures, fmt.Sprintf("edit #%d: oldString and newString must differ", i))
			continue
		}

		matched := e.OldString
		count := strings.Count(content, matched)
		if count == 0 {
			matched, count = tryFuzzyMatch(content, e.OldString)
		}
		if count == 0 {
			failures = append(failures, fmt.Sprintf("edit #%d: oldString not found", i))
			continue
		}
		if count > 1 {
			failures = append(failures, fmt.Sprintf("edit #%d: found %d matches — use more context for disambiguation", i, count))
			continue
		}

		content = strings.Replace(content, matched, e.NewString, 1)
		totalReplaced++
	}

	if totalReplaced == 0 && len(failures) > 0 {
		return "", fmt.Errorf("edit: all %d edits failed:\n%s", len(edits), strings.Join(failures, "\n"))
	}

	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}

	newLines := countLines(content)
	msg := formatEditResult(filePath, totalReplaced, origLines, newLines)
	if len(failures) > 0 {
		msg += fmt.Sprintf("\n%d edit(s) failed:\n%s", len(failures), strings.Join(failures, "\n"))
	}
	return msg, nil
}

// tryFuzzyMatch attempts progressively looser matching strategies when exact
// match fails. Returns the matched string and count.
func tryFuzzyMatch(content, oldStr string) (string, int) {
	strategies := []struct {
		name string
		fn   func(string) string
	}{
		{"trailing-ws-stripped", func(s string) string {
			lines := strings.Split(s, "\n")
			for i, l := range lines {
				lines[i] = strings.TrimRight(l, " \t")
			}
			return strings.Join(lines, "\n")
		}},
		{"smart-quote-normalized", func(s string) string {
			r := strings.NewReplacer(
				"\u201c", `"`, "\u201d", `"`,
				"\u2018", "'", "\u2019", "'",
				"\u00ab", `"`, "\u00bb", `"`,
				"\u201e", `"`, "\u201a", "'",
				"\u2039", "'", "\u203a", "'",
			)
			return r.Replace(s)
		}},
		{"dash-normalized", func(s string) string {
			r := strings.NewReplacer(
				"\u2013", "-", "\u2014", "--",
				"\u2015", "--", "\u2212", "-",
				"\u2010", "-", "\u2011", "-",
				"\u2012", "-",
			)
			return r.Replace(s)
		}},
		{"whitespace-collapsed", func(s string) string {
			lines := strings.Split(s, "\n")
			for i, l := range lines {
				var collapsed strings.Builder
				inSpace := false
				for _, r := range l {
					if r == ' ' || r == '\t' {
						if !inSpace {
							collapsed.WriteByte(' ')
							inSpace = true
						}
					} else {
						collapsed.WriteRune(r)
						inSpace = false
					}
				}
				lines[i] = strings.TrimSpace(collapsed.String())
			}
			return strings.Join(lines, "\n")
		}},
		{"leading-whitespace-normalized", func(s string) string {
			lines := strings.Split(s, "\n")
			for i, l := range lines {
				lines[i] = normalizeLeadingWS(l)
			}
			return strings.Join(lines, "\n")
		}},
	}

	for _, st := range strategies {
		normalizedContent := st.fn(content)
		normalizedOld := st.fn(oldStr)
		pos := strings.Index(normalizedContent, normalizedOld)
		if pos < 0 {
			continue
		}
		count := strings.Count(normalizedContent, normalizedOld)
		if count > 1 {
			return oldStr, count
		}

		start := contentBytePos(content, st.fn, pos)
		end := start
		for end <= len(content) {
			if st.fn(content[start:end]) == normalizedOld {
				return content[start:end], 1
			}
			end++
			if end-start > len(oldStr)*3 {
				break
			}
		}
	}
	return oldStr, 0
}

// normalizeLeadingWS converts leading whitespace on each line to a canonical
// space-based form. Tabs are replaced with spaces at tabstop=4 and trailing
// whitespace within the leading prefix is trimmed. This allows fuzzy matching
// when the model provides indentation in spaces but the file uses tabs
// (or vice versa).
func normalizeLeadingWS(line string) string {
	if line == "" {
		return line
	}
	bodyStart := 0
	for bodyStart < len(line) && (line[bodyStart] == ' ' || line[bodyStart] == '\t') {
		bodyStart++
	}
	if bodyStart == 0 {
		return line
	}
	leading := line[:bodyStart]
	rest := line[bodyStart:]

	// Convert tabs to 4-space equivalent, strip trailing spaces from prefix.
	normalized := strings.ReplaceAll(leading, "\t", "    ")
	normalized = strings.TrimRight(normalized, " ")
	if normalized == "" {
		return rest
	}
	return normalized + rest
}

// contentBytePos returns the byte position in the original content
// corresponding to the given normalized position, using the provided
// normalizing function.
func contentBytePos(content string, fn func(string) string, normPos int) int {
	for i := range len(content) {
		if len(fn(content[:i])) >= normPos {
			return i
		}
	}
	return len(content)
}

// countLines returns the number of lines in a string.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// formatEditResult formats the result of an edit operation with diff info.
func formatEditResult(filePath string, replaced int, origLines, newLines int) string {
	if origLines != newLines {
		delta := newLines - origLines
		verb := "added"
		if delta < 0 {
			verb = "removed"
			delta = -delta
		}
		return fmt.Sprintf("Replaced %d occurrence(s) in %s (%d → %d lines, %s %d)",
			replaced, filePath, origLines, newLines, verb, delta)
	}
	return fmt.Sprintf("Replaced %d occurrence(s) in %s (%d lines)",
		replaced, filePath, origLines)
}
