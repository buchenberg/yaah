package llm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/buchenberg/yaah/internal/types"
)

// Attribute-style tool-call dialect: a function tag carrying the tool name
// after an equals sign, parameter tags carrying the argument name after an
// equals sign, optionally wrapped in a tool_response block. Local models
// drift into this dialect under high tool/context load (issue #314) and
// inference servers return it as plain content instead of tool_calls.
var (
	attrWrapperRe   = regexp.MustCompile(`</?tool_response\s*>`)
	attrFuncOpenRe  = regexp.MustCompile(`<function=([^>\s]+)\s*>`)
	attrFuncCloseRe = regexp.MustCompile(`</function\s*>`)
	attrParamRe     = regexp.MustCompile(`(?s)<parameter=([^>\s]+)\s*>(.*?)</parameter\s*>`)
	attrParamOpenRe = regexp.MustCompile(`<parameter=([^>\s]+)\s*>`)
	attrFuncBlockRe = regexp.MustCompile(`(?s)<function=([^>\s]+)\s*>.*?(?:</function\s*>|\z)`)
)

// parseAttrToolCalls detects attribute-style tool-call markup leaked into
// content and converts it to proper tool calls. Returns cleaned content,
// parsed tool calls, and whether any markup was found.
//
// Handles complete function blocks, truncated blocks where the closing
// tags are missing (an unterminated function body extends to the end of
// the content and complete parameter tags within are salvaged), multiple
// calls in one message, multi-line parameter values, and the unwrapped
// form without the tool_response block.
func parseAttrToolCalls(content string, attrSeq *int) (string, []types.ToolCall, bool) {
	var calls []types.ToolCall
	var kept strings.Builder

	rest := content
	for {
		m := attrFuncOpenRe.FindStringSubmatchIndex(rest)
		if m == nil {
			kept.WriteString(rest)
			break
		}
		kept.WriteString(rest[:m[0]])
		name := rest[m[2]:m[3]]
		body := rest[m[1]:]
		closeLoc := attrFuncCloseRe.FindStringIndex(body)
		if closeLoc == nil {
			// Truncated block: no closing tag, the body extends to the
			// end of the content. Salvage what is complete inside.
			calls = append(calls, buildAttrCall(name, body, attrSeq, len(calls)))
			rest = ""
			break
		}
		calls = append(calls, buildAttrCall(name, body[:closeLoc[0]], attrSeq, len(calls)))
		rest = body[closeLoc[1]:]
	}

	if len(calls) == 0 {
		return content, nil, false
	}
	cleaned := strings.TrimSpace(attrWrapperRe.ReplaceAllString(kept.String(), ""))
	return cleaned, calls, true
}

// buildAttrCall converts one function-tag body into a tool call. The
// parameters become a JSON object; a value that is itself a valid JSON
// object or array is embedded verbatim, everything else is kept as a
// trimmed string (the dialect carries no type markers).
func buildAttrCall(name, body string, attrSeq *int, index int) types.ToolCall {
	args := make(map[string]any)
	for _, pm := range attrParamRe.FindAllStringSubmatch(body, -1) {
		args[pm[1]] = attrParamValue(pm[2])
	}

	argsJSON, err := json.Marshal(args)
	if err != nil {
		argsJSON = []byte("{}")
	}

	id := fmt.Sprintf("attr_%d", *attrSeq)
	*attrSeq++

	return types.ToolCall{
		ID:    id,
		Index: index,
		Type:  "function",
		Function: types.ToolCallFn{
			Name:      name,
			Arguments: string(argsJSON),
		},
	}
}

func attrParamValue(raw string) any {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') && json.Valid([]byte(trimmed)) {
		var v any
		if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
			return v
		}
	}
	return trimmed
}

// recoverDialectToolCalls runs every content-level dialect recovery parser
// over an assistant message whose tool-call markup leaked into text
// instead of structured tool_calls: DeepSeek's DSML format (dsml.go)
// first, then the attribute-style dialect (attrdialect.go). Returns the
// cleaned content, the recovered tool calls, and whether any parser found
// markup. Both parsers share one monotonic ID counter; the ID prefixes
// keep the recovered calls distinguishable by origin.
func recoverDialectToolCalls(content string, seq *int) (string, []types.ToolCall, bool) {
	cleaned, calls, ok := parseDSMLToolCalls(content, seq)
	if attrCleaned, attrCalls, attrOK := parseAttrToolCalls(cleaned, seq); attrOK {
		return attrCleaned, append(calls, attrCalls...), true
	}
	return cleaned, calls, ok
}
