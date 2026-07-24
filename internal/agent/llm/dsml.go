package llm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/buchenberg/yaah/internal/types"
)

const dsmlOpenTag  = "<\uff5c\uff5cDSML\uff5c\uff5ctool_calls>"
const dsmlCloseTag = "</\uff5c\uff5cDSML\uff5c\uff5ctool_calls>"

var (
	dsmlBlockRe  = regexp.MustCompile(`(?s)\n?<\x{FF5C}\x{FF5C}DSML\x{FF5C}\x{FF5C}tool_calls>(.*?)</\x{FF5C}\x{FF5C}DSML\x{FF5C}\x{FF5C}tool_calls>\n?`)
	dsmlOpenRe   = regexp.MustCompile(`\n?<\x{FF5C}\x{FF5C}DSML\x{FF5C}\x{FF5C}tool_calls>`)
	dsmlInvokeRe = regexp.MustCompile(`(?s)<\x{FF5C}\x{FF5C}DSML\x{FF5C}\x{FF5C}invoke\s+name="([^"]+)">(.*?)</\x{FF5C}\x{FF5C}DSML\x{FF5C}\x{FF5C}invoke>`)
	dsmlParamRe  = regexp.MustCompile(`(?s)<\x{FF5C}\x{FF5C}DSML\x{FF5C}\x{FF5C}parameter\s+name="([^"]+)"(?:\s+string="(true|false)")?>(.*?)</\x{FF5C}\x{FF5C}DSML\x{FF5C}\x{FF5C}parameter>`)
)

// dsmlTokenFilter strips DSML markup from streaming tokens in real-time so
// that OnToken callbacks never see leaked DSML content. It statefully
// tracks DSML block boundaries across chunk boundaries.
type dsmlTokenFilter struct {
	inBlock bool
	pending []byte
}

// filterToken processes a streaming content delta and returns the portion
// that is safe to forward to OnToken. Returns an empty string when the
// delta is fully suppressed (inside a DSML block).
func (f *dsmlTokenFilter) filterToken(token string) string {
	f.pending = append(f.pending, token...)

	if f.inBlock {
		if idx, rest := f.scanEnd(); idx >= 0 {
			f.inBlock = false
			f.pending = rest
			return f.filterToken("")
		}
		hold := trailingPrefixLen(f.pending, dsmlCloseTag)
		if hold > 0 {
			f.pending = f.pending[len(f.pending)-hold:]
		} else {
			f.pending = nil
		}
		return ""
	}

	if idx := f.scanStart(); idx >= 0 {
		f.inBlock = true
		before := f.pending[:idx]
		f.pending = f.pending[idx+len(dsmlOpenTag):]
		if after := f.filterToken(""); after != "" {
			return string(before) + after
		}
		return string(before)
	}

	hold := trailingPrefixLen(f.pending, dsmlOpenTag)
	output := string(f.pending[:len(f.pending)-hold])
	f.pending = f.pending[len(f.pending)-hold:]
	return output
}

func (f *dsmlTokenFilter) scanStart() int {
	return bytesIndex(f.pending, dsmlOpenTag)
}

func (f *dsmlTokenFilter) scanEnd() (int, []byte) {
	idx := bytesIndex(f.pending, dsmlCloseTag)
	if idx < 0 {
		return -1, nil
	}
	rest := f.pending[idx+len(dsmlCloseTag):]
	return idx, rest
}

func trailingPrefixLen(data []byte, tag string) int {
	maxN := len(data)
	if maxN > len(tag)-1 {
		maxN = len(tag) - 1
	}
	for n := maxN; n > 0; n-- {
		candidate := data[len(data)-n:]
		matched := true
		for j := 0; j < len(candidate); j++ {
			if candidate[j] != tag[j] {
				matched = false
				break
			}
		}
		if matched {
			return n
		}
	}
	return 0
}

func bytesIndex(data []byte, substr string) int {
	for i := 0; i <= len(data)-len(substr); i++ {
		if string(data[i:i+len(substr)]) == substr {
			return i
		}
	}
	return -1
}

// parseDSMLToolCalls detects DeepSeek's native DSML tool-call markup leaked
// into content and converts it to proper tool calls. Returns cleaned content,
// parsed tool calls, and whether any DSML was found.
//
// Handles both complete DSML blocks (with closing tag) and truncated blocks
// where the stream was cut off mid-markup. In the truncated case, everything
// from the opening tag onward is stripped and any complete invoke blocks
// within are salvaged.
func parseDSMLToolCalls(content string) (string, []types.ToolCall, bool) {
	block := dsmlBlockRe.FindString(content)
	if block == "" {
		// Fallback: truncated DSML block (opening tag present, closing tag missing).
		if loc := dsmlOpenRe.FindStringIndex(content); loc != nil {
			cleaned := strings.TrimSpace(content[:loc[0]])
			truncated := content[loc[1]:]
			calls := parseDSMLInvokes(truncated)
			return cleaned, calls, true
		}
		return content, nil, false
	}

	cleaned := strings.TrimSpace(dsmlBlockRe.ReplaceAllString(content, ""))
	calls := parseDSMLInvokes(block)
	return cleaned, calls, len(calls) > 0
}

func parseDSMLInvokes(block string) []types.ToolCall {
	var calls []types.ToolCall
	for i, m := range dsmlInvokeRe.FindAllStringSubmatch(block, -1) {
		name := m[1]
		body := m[2]

		args := make(map[string]any)
		for _, pm := range dsmlParamRe.FindAllStringSubmatch(body, -1) {
			paramName := pm[1]
			isString := pm[2] != "false"
			paramVal := pm[3]
			if isString {
				args[paramName] = paramVal
			} else if n, err := strconv.ParseFloat(paramVal, 64); err == nil {
				args[paramName] = n
			} else {
				args[paramName] = paramVal
			}
		}

		argsJSON, err := json.Marshal(args)
		if err != nil {
			argsJSON = []byte("{}")
		}

		calls = append(calls, types.ToolCall{
			ID:    fmt.Sprintf("dsml_%d", i),
			Index: i,
			Type:  "function",
			Function: types.ToolCallFn{
				Name:      name,
				Arguments: string(argsJSON),
			},
		})
	}
	return calls
}
