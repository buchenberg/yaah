package llm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/buchenberg/yaah/internal/types"
)

// Attribute-style tool-call dialect: a function tag carrying the tool name
// after an equals sign, parameter tags carrying the argument name after an
// equals sign, wrapped in a tool_call or tool_response block. Local models
// drift into this dialect under high tool/context load (issue #314) and
// inference servers return it as plain content instead of tool_calls.
var (
	attrWrapperRe   = regexp.MustCompile(`</?tool_(?:call|response)\s*>`)
	attrFuncOpenRe  = regexp.MustCompile(`<function=([^>\s]+)\s*>`)
	attrFuncCloseRe = regexp.MustCompile(`</function\s*>`)
	attrParamRe     = regexp.MustCompile(`(?s)<parameter=([^>\s]+)\s*>(.*?)</parameter\s*>`)
	attrParamOpenRe = regexp.MustCompile(`<parameter=([^>\s]+)\s*>`)
	attrFuncBlockRe = regexp.MustCompile(`(?s)<function=([^>\s]+)\s*>.*?(?:</function\s*>|\z)`)
)

// attrSchema is the parameter schema of one offered tool: the declared
// JSON type per property plus the required-property list. The dialect
// carries no type markers, so the schema is what turns "15" into the
// integer a bash timeout field decodes, and keeps a write body that
// happens to look like JSON a string (#333 review).
type attrSchema struct {
	props    map[string]string
	required []string
}

// attrToolSchemas indexes the offered tools' JSON parameter schemas by
// function name. Tools whose schema fails to parse are simply absent —
// recovery then falls back to the typeless heuristic.
func attrToolSchemas(tools []types.ToolDef) map[string]*attrSchema {
	if len(tools) == 0 {
		return nil
	}
	schemas := make(map[string]*attrSchema, len(tools))
	for _, td := range tools {
		if td.Function.Name == "" || len(td.Function.Parameters) == 0 {
			continue
		}
		var ps struct {
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(td.Function.Parameters, &ps); err != nil {
			continue
		}
		sc := &attrSchema{props: make(map[string]string, len(ps.Properties)), required: ps.Required}
		for name, p := range ps.Properties {
			sc.props[name] = p.Type
		}
		schemas[td.Function.Name] = sc
	}
	return schemas
}

// attrProtectedSpans returns the byte spans of content that must never be
// mined for dialect markup: fenced code blocks (``` or ~~~, terminated or
// running to the end) and markdown blockquote lines. A report that quotes
// an example call is prose; recovering the quote into an executable tool
// call would dispatch a command nobody asked for (#333 review).
func attrProtectedSpans(content string) [][2]int {
	var spans [][2]int
	pos := 0
	fence := "" // open fence marker, empty when outside a fence
	fenceStart := 0
	for pos < len(content) {
		lineEnd := strings.IndexByte(content[pos:], '\n')
		var line string
		next := len(content)
		if lineEnd >= 0 {
			line = content[pos : pos+lineEnd]
			next = pos + lineEnd + 1
		} else {
			line = content[pos:]
		}
		trimmed := strings.TrimLeft(line, " \t")
		switch {
		case fence != "":
			if strings.HasPrefix(trimmed, fence) && strings.TrimSpace(trimmed[len(fence):]) == "" {
				spans = append(spans, [2]int{fenceStart, next})
				fence = ""
			}
		case strings.HasPrefix(trimmed, "```"), strings.HasPrefix(trimmed, "~~~"):
			fence = "```"
			if trimmed[0] == '~' {
				fence = "~~~"
			}
			fenceStart = pos
		case strings.HasPrefix(trimmed, ">"):
			spans = append(spans, [2]int{pos, next})
		}
		if lineEnd < 0 {
			break
		}
		pos = next
	}
	if fence != "" {
		// Unterminated fence: everything to the end is example text.
		spans = append(spans, [2]int{fenceStart, len(content)})
	}
	return spans
}

func attrInSpan(abs int, spans [][2]int) bool {
	for _, s := range spans {
		if abs >= s[0] && abs < s[1] {
			return true
		}
	}
	return false
}

// attrAtLineStart reports whether the match at m[0] in s begins its line
// (only whitespace before it). Real dialect leaks put the function tag on
// its own line; a mid-line mention is prose quoting the markup.
func attrAtLineStart(s string, idx int) bool {
	for i := idx - 1; i >= 0; i-- {
		switch s[i] {
		case '\n':
			return true
		case ' ', '\t', '\r':
		default:
			return false
		}
	}
	return true
}

// parseAttrToolCalls detects attribute-style tool-call markup leaked into
// content and converts it to proper tool calls. Returns cleaned content,
// parsed tool calls, rejection reasons, and whether any markup was found.
//
// Handles complete function blocks, truncated blocks where the closing
// tags are missing (an unterminated function body extends to the end of
// the content and complete parameter tags within are salvaged), multiple
// calls in one message, multi-line parameter values, and the unwrapped
// form without the tool_call/tool_response block.
//
// Two safety gates decide whether a detected block may become a call
// (#333 review): markup inside fenced code blocks or blockquotes is a
// quoted example and stays prose, and a function tag must start its line
// — mid-line mentions are prose too. A block that fails a gate is kept
// verbatim in the content, where the report guard can still flag it.
//
// A block that passes the gates is still rejected — reported in the
// rejection list instead of synthesized — when its body carries an
// unterminated parameter (dispatching the partial arguments of a
// truncated write would erase the file) or when the offered tool's schema
// declares a required argument the block does not provide. Rejections
// surface as explicit dispatch errors at the recovery seam; a partial
// call is never silently executed or silently dropped.
func parseAttrToolCalls(content string, attrSeq *int, tools []types.ToolDef) (string, []types.ToolCall, []string, bool) {
	protected := attrProtectedSpans(content)
	schemas := attrToolSchemas(tools)

	var calls []types.ToolCall
	var rejected []string
	var kept strings.Builder
	found := false

	base := 0 // absolute offset of rest within content
	rest := content
	for {
		m := attrFuncOpenRe.FindStringSubmatchIndex(rest)
		if m == nil {
			kept.WriteString(rest)
			break
		}
		if attrInSpan(base+m[0], protected) || !attrAtLineStart(rest, m[0]) {
			// Quoted, fenced, or mid-line example: keep verbatim, scan on.
			kept.WriteString(rest[:m[1]])
			base += m[1]
			rest = rest[m[1]:]
			continue
		}
		found = true
		kept.WriteString(rest[:m[0]])
		name := rest[m[2]:m[3]]
		body := rest[m[1]:]
		closeLoc := attrFuncCloseRe.FindStringIndex(body)
		if closeLoc == nil {
			// Truncated block: no closing tag, the body extends to the
			// end of the content. Salvage what is safely complete.
			if call, reason := buildAttrCall(name, body, attrSeq, len(calls), schemas); reason != "" {
				rejected = append(rejected, reason)
				// Keep the full markup so the report guard still sees it.
				kept.WriteString(rest[m[0]:])
			} else {
				calls = append(calls, call)
			}
			break
		}
		if call, reason := buildAttrCall(name, body[:closeLoc[0]], attrSeq, len(calls), schemas); reason != "" {
			rejected = append(rejected, reason)
			kept.WriteString(rest[m[0] : m[1]+closeLoc[1]])
		} else {
			calls = append(calls, call)
		}
		base += m[1] + closeLoc[1]
		rest = body[closeLoc[1]:]
	}

	if !found {
		return content, nil, nil, false
	}
	cleaned := strings.TrimSpace(attrWrapperRe.ReplaceAllString(kept.String(), ""))
	return cleaned, calls, rejected, true
}

// buildAttrCall converts one function-tag body into a tool call, or
// returns a non-empty rejection reason when synthesizing would be unsafe:
// an unterminated parameter tag (the stream was cut mid-value, so the
// recovered arguments would be silently partial) or a required argument
// missing against the offered tool's schema.
func buildAttrCall(name, body string, attrSeq *int, index int, schemas map[string]*attrSchema) (types.ToolCall, string) {
	// An open parameter tag with no close means the value was cut off
	// mid-stream. Dropping it would dispatch a call missing an argument
	// the model was still writing — a write with only filePath and no
	// content defaults to "" and erases the file (#333 review).
	if residual := attrParamRe.ReplaceAllString(body, ""); attrParamOpenRe.MatchString(residual) {
		return types.ToolCall{}, fmt.Sprintf("tool call %q was cut off mid-parameter — refusing to dispatch partial arguments", name)
	}

	var sc *attrSchema
	if schemas != nil {
		sc = schemas[name]
	}

	args := make(map[string]any)
	for _, pm := range attrParamRe.FindAllStringSubmatch(body, -1) {
		declared := ""
		if sc != nil {
			declared = sc.props[pm[1]]
		}
		args[pm[1]] = attrParamValue(pm[2], declared)
	}
	if sc != nil {
		for _, req := range sc.required {
			if _, ok := args[req]; !ok {
				return types.ToolCall{}, fmt.Sprintf("tool call %q is missing required argument %q", name, req)
			}
		}
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
	}, ""
}

// attrParamValue coerces one dialect parameter to the type the tool's
// schema declares: strings stay strings even when they look like JSON (a
// write body that is a JSON document must not become an object), integers
// and numbers parse, booleans parse. Values that fail their declared
// type, and tools with no schema, fall back to the typeless heuristic: a
// valid JSON object or array embeds verbatim, everything else is kept as
// a trimmed string.
func attrParamValue(raw, declared string) any {
	trimmed := strings.TrimSpace(raw)
	switch declared {
	case "string":
		return trimmed
	case "integer":
		if n, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return n
		}
	case "number":
		if f, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return f
		}
	case "boolean":
		if b, err := strconv.ParseBool(trimmed); err == nil {
			return b
		}
	}
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
// cleaned content, the recovered tool calls, rejection reasons for markup
// that could not be safely synthesized, and whether any parser found
// markup. Both parsers share one monotonic ID counter; the ID prefixes
// keep the recovered calls distinguishable by origin. The offered tool
// schemas type and validate the attribute dialect's untyped parameters.
//
// A non-empty rejection list means the caller must fail the turn
// explicitly: part of the model's intent could not be dispatched, and
// neither executing the remainder nor accepting the content as the answer
// is a truthful outcome.
func recoverDialectToolCalls(content string, seq *int, tools []types.ToolDef) (string, []types.ToolCall, []string, bool) {
	cleaned, calls, dsmlRejected, ok := parseDSMLToolCalls(content, seq)
	attrCleaned, attrCalls, attrRejected, attrOK := parseAttrToolCalls(cleaned, seq, tools)
	rejected := append(dsmlRejected, attrRejected...)
	if attrOK {
		return attrCleaned, append(calls, attrCalls...), rejected, true
	}
	return cleaned, calls, rejected, ok
}
