package llm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/buchenberg/yaah/internal/types"
)

var (
	dsmlBlockRe  = regexp.MustCompile(`(?s)\n?<\x{FF5C}\x{FF5C}DSML\x{FF5C}\x{FF5C}tool_calls>(.*?)</\x{FF5C}\x{FF5C}DSML\x{FF5C}\x{FF5C}tool_calls>\n?`)
	dsmlOpenRe   = regexp.MustCompile(`\n?<\x{FF5C}\x{FF5C}DSML\x{FF5C}\x{FF5C}tool_calls>`)
	dsmlInvokeRe = regexp.MustCompile(`(?s)<\x{FF5C}\x{FF5C}DSML\x{FF5C}\x{FF5C}invoke\s+name="([^"]+)">(.*?)</\x{FF5C}\x{FF5C}DSML\x{FF5C}\x{FF5C}invoke>`)
	dsmlParamRe  = regexp.MustCompile(`(?s)<\x{FF5C}\x{FF5C}DSML\x{FF5C}\x{FF5C}parameter\s+name="([^"]+)"(?:\s+string="(true|false)")?>(.*?)</\x{FF5C}\x{FF5C}DSML\x{FF5C}\x{FF5C}parameter>`)
)

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
