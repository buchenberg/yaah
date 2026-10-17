package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/buchenberg/yaah/internal/prompts"
)

// editEntry is a single edit operation within a multi-edit call.
type editEntry struct {
	OldString string `json:"oldString"`
	NewString string `json:"newString"`
}

// EditTool performs exact string replacements in a file, with fuzzy fallback
// when exact match fails. Supports multi-edit via an edits[] array.
type EditTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*EditTool)(nil)
	_ WorkspaceSetter     = (*EditTool)(nil)
)

func (t *EditTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *EditTool) SetWorkspace(ws Workspace)          { t.WS = ws }

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
	ws := workspaceOf(t.WS, t.PV)
	resolved, err := ws.ResolvePath(params.FilePath)
	if err != nil {
		return "", err
	}
	params.FilePath = resolved

	if len(params.Edits) > 0 {
		return t.executeMultiEdit(ctx, ws, params.FilePath, params.Edits)
	}

	if params.OldString == "" {
		return "", fmt.Errorf("edit: oldString or edits[] is required")
	}
	if params.OldString == params.NewString {
		return "", fmt.Errorf("edit: oldString and newString must differ")
	}

	return t.executeSingleEdit(ctx, ws, params.FilePath, params.OldString, params.NewString, params.ReplaceAll)
}

func (t *EditTool) executeSingleEdit(ctx context.Context, ws Workspace, filePath, oldStr, newStr string, replaceAll bool) (string, error) {
	data, err := ws.ReadFile(ctx, filePath)
	if err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}

	crlf := strings.Contains(string(data), "\r\n")
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	oldStr = strings.ReplaceAll(oldStr, "\r\n", "\n")
	newStr = strings.ReplaceAll(newStr, "\r\n", "\n")

	origLines := countLines(content)

	matched, count, usedFuzzy, decoded := matchEdit(content, oldStr)
	if count == 0 {
		hint := ""
		if d, ok := decodeLiteralEscapes(oldStr); ok && d != oldStr {
			hint = " (also tried decoding literal escape sequences in oldString)"
		}
		return "", fmt.Errorf("edit: oldString not found in %s%s", filePath, hint)
	}

	// newString is never decoded. When oldString had to be decoded, the model
	// may well have double-escaped the replacement too — but it may equally
	// have sent a literal backslash that is meant to survive (a Windows path, a
	// regex, source containing "\n"), and decoding would silently corrupt it.
	// Write it verbatim and say so, so the caller can re-issue if needed.
	newHasEscapes := false
	if decoded {
		if _, ok := decodeLiteralEscapes(newStr); ok {
			newHasEscapes = true
		}
	}

	// If fuzzy matching was used, verify the mapped-back match actually
	// exists in the original content the expected number of times.
	// This prevents silent corruption when fuzzy match maps to wrong location.
	if usedFuzzy {
		actualCount := strings.Count(content, matched)
		if actualCount == 0 {
			return "", fmt.Errorf("edit: fuzzy match found candidate but it does not exist in original file (oldString: %q)", oldStr)
		}
		if !replaceAll && actualCount > 1 {
			return "", fmt.Errorf("edit: fuzzy match produced %d matches in original file; use replaceAll or provide more context (oldString: %q)", actualCount, oldStr)
		}
		count = actualCount
	}

	if !replaceAll {
		if count > 1 {
			return "", fmt.Errorf("edit: found %d matches for oldString in %s; use replaceAll or provide more context", count, filePath)
		}
		content = strings.Replace(content, matched, newStr, 1)
	} else {
		content = strings.ReplaceAll(content, matched, newStr)
	}

	if crlf {
		content = strings.ReplaceAll(content, "\n", "\r\n")
	}
	if err := ws.WriteFile(ctx, filePath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}

	replaced := count
	if !replaceAll {
		replaced = 1
	}
	newLines := countLines(content)
	msg := formatEditResult(filePath, replaced, origLines, newLines)
	msg += decodeNote(decoded, newHasEscapes)
	return msg, nil
}

func (t *EditTool) executeMultiEdit(ctx context.Context, ws Workspace, filePath string, edits []editEntry) (string, error) {
	data, err := ws.ReadFile(ctx, filePath)
	if err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}

	crlf := strings.Contains(string(data), "\r\n")
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	for i := range edits {
		edits[i].OldString = strings.ReplaceAll(edits[i].OldString, "\r\n", "\n")
		edits[i].NewString = strings.ReplaceAll(edits[i].NewString, "\r\n", "\n")
	}

	origLines := countLines(content)
	totalReplaced := 0
	decodedAny := false
	newHasEscapes := false
	var failures []string

	for i, e := range edits {
		if e.OldString == e.NewString {
			failures = append(failures, fmt.Sprintf("edit #%d: oldString and newString must differ", i))
			continue
		}

		matched, count, usedFuzzy, decoded := matchEdit(content, e.OldString)
		if count == 0 {
			failures = append(failures, fmt.Sprintf("edit #%d: oldString not found", i))
			continue
		}
		if _, ok := decodeLiteralEscapes(e.NewString); ok {
			newHasEscapes = true
		}

		// If fuzzy matching was used, verify the mapped-back match actually
		// exists in the original content the expected number of times.
		if usedFuzzy {
			actualCount := strings.Count(content, matched)
			if actualCount == 0 {
				failures = append(failures, fmt.Sprintf("edit #%d: fuzzy match found candidate but it does not exist in original file", i))
				continue
			}
			if actualCount > 1 {
				failures = append(failures, fmt.Sprintf("edit #%d: fuzzy match produced %d matches — use more context for disambiguation", i, actualCount))
				continue
			}
			count = actualCount
		}

		if count > 1 {
			failures = append(failures, fmt.Sprintf("edit #%d: found %d matches — use more context for disambiguation", i, count))
			continue
		}

		content = strings.Replace(content, matched, e.NewString, 1)
		totalReplaced++
		decodedAny = decodedAny || decoded
	}

	if totalReplaced == 0 && len(failures) > 0 {
		return "", fmt.Errorf("edit: all %d edits failed:\n%s", len(edits), strings.Join(failures, "\n"))
	}

	if crlf {
		content = strings.ReplaceAll(content, "\n", "\r\n")
	}
	if err := ws.WriteFile(ctx, filePath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}

	newLines := countLines(content)
	msg := formatEditResult(filePath, totalReplaced, origLines, newLines)
	msg += decodeNote(decodedAny, newHasEscapes)
	if len(failures) > 0 {
		msg += fmt.Sprintf("\n%d edit(s) failed:\n%s", len(failures), strings.Join(failures, "\n"))
	}
	return msg, nil
}

// matchEdit locates oldStr in content, preferring an exact match, then the
// fuzzy fallbacks, and finally an interpretation where literal escape
// sequences (\u00e9, \n, \t) are decoded into the characters they stand for.
//
// The last step exists because a model sometimes double-escapes a tool
// argument: it sends "\\u00e9" meaning "é", and the JSON layer faithfully
// delivers the six literal characters. Decoding only runs when nothing else
// matched, and callers report it in the result, so it cannot silently rewrite a
// file that already contains the literal escape text.
func matchEdit(content, oldStr string) (matched string, count int, usedFuzzy, decoded bool) {
	if c := strings.Count(content, oldStr); c > 0 {
		return oldStr, c, false, false
	}
	if m, c := tryFuzzyMatch(content, oldStr); c > 0 {
		return m, c, true, false
	}
	if d, ok := decodeLiteralEscapes(oldStr); ok {
		// The file content was normalized to LF before matching; a decoded
		// "\r\n" must be normalized the same way or it can never match a CRLF
		// file (and would reinstate CR into an LF buffer if it did).
		d = strings.ReplaceAll(d, "\r\n", "\n")
		if c := strings.Count(content, d); c > 0 {
			return d, c, false, true
		}
		if m, c := tryFuzzyMatch(content, d); c > 0 {
			return m, c, true, true
		}
	}
	return oldStr, 0, false, false
}

// decodeNote describes any escape interpretation applied to an edit. It is
// appended to the success message so a model that double-escaped its arguments
// can see what happened without re-reading the file.
func decodeNote(decodedOld, escapedNew bool) string {
	var b strings.Builder
	if decodedOld {
		b.WriteString("\nnote: oldString contained literal escape sequences (e.g. \\u00e9); they were decoded to match the file")
	}
	if escapedNew {
		b.WriteString("\nnote: newString contains literal escape sequences and was written verbatim")
	}
	return b.String()
}

// decodeLiteralEscapes interprets JSON-style backslash escapes the way a JSON
// string decoder would, returning the decoded text and whether anything
// changed. Unknown or malformed escapes are preserved verbatim. Surrogate
// pairs are combined; an unpaired surrogate is left alone rather than turned
// into U+FFFD, because that would corrupt the text instead of matching it.
func decodeLiteralEscapes(s string) (string, bool) {
	if !strings.Contains(s, `\`) {
		return s, false
	}
	var b strings.Builder
	b.Grow(len(s))
	changed := false

	for i := 0; i < len(s); {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}
		switch s[i+1] {
		case 'n':
			b.WriteByte('\n')
			i += 2
			changed = true
		case 't':
			b.WriteByte('\t')
			i += 2
			changed = true
		case 'r':
			b.WriteByte('\r')
			i += 2
			changed = true
		case 'b':
			b.WriteByte('\b')
			i += 2
			changed = true
		case 'f':
			b.WriteByte('\f')
			i += 2
			changed = true
		case '"':
			b.WriteByte('"')
			i += 2
			changed = true
		case '/':
			b.WriteByte('/')
			i += 2
			changed = true
		case '\\':
			b.WriteByte('\\')
			i += 2
			changed = true
		case 'u', 'U':
			n := 4
			if s[i+1] == 'U' {
				n = 8
			}
			v, ok := parseHexEscape(s, i+2, n)
			if !ok || v > utf8.MaxRune {
				// Malformed, or a code point Unicode cannot encode. Preserve the
				// escape verbatim: WriteRune would emit U+FFFD and corrupt the
				// text instead of leaving the source visible.
				b.WriteByte(s[i])
				i++
				continue
			}
			r := rune(v)
			if utf16.IsSurrogate(r) {
				// A high surrogate followed by a \uXXXX low surrogate is one
				// code point. Anything else is left as literal text rather than
				// written as U+FFFD.
				lowPos := i + 2 + n
				var low uint64
				ok := false
				if lowPos+1 < len(s) && s[lowPos] == '\\' && s[lowPos+1] == 'u' {
					low, ok = parseHexEscape(s, lowPos+2, 4)
				}
				if !ok || r < 0xD800 || r > 0xDBFF || rune(low) < 0xDC00 || rune(low) > 0xDFFF {
					b.WriteByte(s[i])
					i++
					continue
				}
				b.WriteRune(utf16.DecodeRune(r, rune(low)))
				i = lowPos + 6
				changed = true
				continue
			}
			b.WriteRune(r)
			i += 2 + n
			changed = true
		default:
			b.WriteByte(s[i]) // unknown escape: keep it as written
			i++
		}
	}
	if !changed {
		return s, false
	}
	return b.String(), true
}

// parseHexEscape parses n hex digits at s[pos:], reporting whether they are
// present and valid.
func parseHexEscape(s string, pos, n int) (uint64, bool) {
	if pos < 0 || pos+n > len(s) {
		return 0, false
	}
	v, err := strconv.ParseUint(s[pos:pos+n], 16, 32)
	if err != nil {
		return 0, false
	}
	return v, true
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
// normalizing function. Uses binary search since len(fn(content[:i]))
// is monotonically non-decreasing.
func contentBytePos(content string, fn func(string) string, normPos int) int {
	lo, hi := 0, len(content)
	for lo < hi {
		mid := (lo + hi) / 2
		if len(fn(content[:mid])) >= normPos {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo
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
