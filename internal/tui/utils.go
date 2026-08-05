package tui

import (
	"regexp"
	"strings"

	"github.com/buchenberg/yaah/internal/banner"
)

var (
	mdLinkRe   = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	autoLinkRe = regexp.MustCompile(`<((?:https?|ftp)://[^>]+)>`)
)

// osc8Link wraps text in an OSC 8 hyperlink for clickable terminal links.
func osc8Link(text, url string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// injectHyperlinks converts markdown links, autolinks, and bare URLs into
// OSC 8 hyperlink sequences before glamour renders them.
func injectHyperlinks(md string) string {
	md = mdLinkRe.ReplaceAllStringFunc(md, func(match string) string {
		parts := mdLinkRe.FindStringSubmatch(match)
		if len(parts) == 3 {
			return osc8Link(parts[1], parts[2])
		}
		return match
	})
	md = autoLinkRe.ReplaceAllStringFunc(md, func(match string) string {
		url := strings.Trim(match, "<>")
		return osc8Link(url, url)
	})
	return md
}

type textSegment struct {
	content string
	isTable bool
}

// splitRow splits a pipe-delimited table row into trimmed columns.
func splitRow(line string) []string {
	line = strings.Trim(line, "| \t")
	cols := strings.Split(line, "|")
	for i := range cols {
		cols[i] = strings.TrimSpace(cols[i])
	}
	return cols
}

func replacePattern(s, open, close string, style func(string) string) string {
	for {
		start := strings.Index(s, open)
		if start == -1 {
			break
		}
		end := strings.Index(s[start+len(open):], close)
		if end == -1 {
			break
		}
		end += start + len(open)
		inner := s[start+len(open) : end]
		styled := style(inner)
		s = s[:start] + styled + s[end+len(close):]
	}
	return s
}

func isWideRune(r rune) bool {
	return r >= 0x1100 && r <= 0x115F ||
		r >= 0x2E80 && r <= 0xA4CF ||
		r >= 0xAC00 && r <= 0xD7A3 ||
		r >= 0xF900 && r <= 0xFAFF ||
		r >= 0xFE30 && r <= 0xFE6F ||
		r >= 0xFF01 && r <= 0xFF60 ||
		r >= 0xFFE0 && r <= 0xFFE6 ||
		r >= 0x1B000 && r <= 0x1B2FF ||
		r >= 0x1F004 && r <= 0x1F251 ||
		r >= 0x20000 && r <= 0x3FFFD
}

// --- list and tree rendering ---

// bulletPattern matches markdown bullet list items (* item, - item, + item).
var bulletPattern = regexp.MustCompile(`(?m)^[*\-+]\s`)

// isListContent detects if content contains bullet list items.
func isListContent(s string) bool {
	return strings.Contains(s, "\n") && bulletPattern.MatchString(s)
}

var treeLineRe = regexp.MustCompile(`[├└]──`)

// isTreeContent detects tree-like content with box-drawing characters.
func isTreeContent(s string) bool {
	return treeLineRe.MatchString(s)
}

// splitTreePrefix separates tree-drawing characters from the node name.
func splitTreePrefix(line string) (prefix, name string) {
	treeChars := map[rune]bool{'│': true, ' ': true, '├': true, '└': true, '─': true, '┬': true}
	runes := []rune(line)
	i := 0
	for i < len(runes) {
		r := runes[i]
		if !treeChars[r] {
			if r == '\\' && i+1 < len(runes) {
				i += 2
				continue
			}
			break
		}
		i++
	}
	return string(runes[:i]), strings.TrimSpace(string(runes[i:]))
}

// treeDepth computes the depth from tree-drawing prefix characters.
func treeDepth(prefix string) int {
	depth := 0
	for _, r := range prefix {
		if r == '│' || r == '├' || r == '└' {
			depth++
		}
	}
	return depth
}

// displayWidth returns the approximate terminal display width of a string,
// skipping ANSI escape sequences so styled text measures correctly.
func displayWidth(s string) int {
	w := 0
	inEscape := false
	for _, r := range s {
		if r == 0x1b { // ESC
			inEscape = true
			continue
		}
		if inEscape {
			if r == '[' {
				continue // CSI sequence continues
			}
			// CSI: skip until final byte (0x40–0x7E)
			if r >= 0x40 && r <= 0x7E {
				inEscape = false
			}
			continue
		}
		if r <= 0x7F {
			w++
		} else if isWideRune(r) {
			w += 2
		} else {
			w++
		}
	}
	return w
}

// chatWrap wraps text to fit within the terminal width, accounting for a
// prefix label (e.g. "yaah: "). It returns the wrapped text with the prefix
// applied only to the first line.
func chatWrap(prefix, content string, width int) string {
	maxWidth := max(width-len(prefix), 10)
	wrapped := wrapText(content, maxWidth)
	lines := strings.Split(wrapped, "\n")
	for i, line := range lines {
		if i == 0 {
			lines[i] = prefix + line
		} else {
			lines[i] = strings.Repeat(" ", len(prefix)) + line
		}
	}
	return strings.Join(lines, "\n")
}

// wrapText performs simple word-wrapping at the given width, preserving
// explicit newlines in the source text.
func wrapText(text string, width int) string {
	var result strings.Builder
	for _, paragraph := range strings.Split(text, "\n") {
		if paragraph == "" {
			result.WriteString("\n")
			continue
		}
		wrapped := wrapParagraph(paragraph, width)
		result.WriteString(wrapped)
		result.WriteString("\n")
	}
	out := result.String()
	// Remove the trailing newline added by the last iteration
	return strings.TrimSuffix(out, "\n")
}

// wrapParagraph wraps a single line (no embedded newlines) to the given width.
func wrapParagraph(line string, width int) string {
	words := strings.Fields(line)
	if len(words) == 0 {
		return ""
	}
	var result strings.Builder
	lineLen := 0
	for i, word := range words {
		wLen := displayWidth(word)
		if i == 0 {
			result.WriteString(word)
			lineLen = wLen
		} else if lineLen+1+wLen > width {
			result.WriteString("\n")
			result.WriteString(word)
			lineLen = wLen
		} else {
			result.WriteString(" ")
			result.WriteString(word)
			lineLen += 1 + wLen
		}
	}
	return result.String()
}

func lolcatRender(text string) string {
	return strings.ReplaceAll(banner.Lolcat(text), "\n", "")
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}
