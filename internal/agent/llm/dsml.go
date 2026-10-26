package llm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/buchenberg/yaah/internal/types"
)

// DeepSeek's DSML tool-call dialect.
//
// When the provider fails to translate a tool call into structured
// tool_calls, the raw markup lands in the assistant content and - without
// recovery - is shown to the user as the answer while the call the model
// intended never runs. Observed emissions are not byte-identical: the
// number of U+FF5C bars around the DSML namespace word varies, a space may
// separate the bars from the element name, and the block element is
// spelled calls, tool_calls or function_calls depending on the model (a
// leaked production sample used "<|DSML| calls>" with a leading space and
// no tool_ prefix). Every matcher below is therefore built from these
// fragments, so the grammar is defined exactly once.
const (
	dsmlBars = `\x{FF5C}+`

	// Block wrapper around a group of invokes.
	dsmlCallsOpen  = `<` + dsmlBars + `DSML` + dsmlBars + `\s*(?:tool_|function_)?calls\s*>`
	dsmlCallsClose = `</` + dsmlBars + `DSML` + dsmlBars + `\s*(?:tool_|function_)?calls\s*>`

	// Invoke element: one tool call, with its name in a quoted attribute.
	dsmlInvokeHead  = `<` + dsmlBars + `DSML` + dsmlBars + `\s*(?:invoke|tool_call|function_call)\s+name=`
	dsmlInvokeClose = `</` + dsmlBars + `DSML` + dsmlBars + `\s*(?:invoke|tool_call|function_call)\s*>`

	// Parameter element: one argument of the enclosing invoke.
	dsmlParamHead  = `<` + dsmlBars + `DSML` + dsmlBars + `\s*parameter\s+name=`
	dsmlParamClose = `</` + dsmlBars + `DSML` + dsmlBars + `\s*parameter\s*>`
)

// Rejection reasons for markup that is present but cannot be dispatched.
// A non-empty rejection means the caller must fail the turn: part of the
// model's intent is unavailable, so neither executing the remainder nor
// presenting the content as the answer is truthful.
const (
	rejectDSMLNoInvoke = "DSML tool-call block contained no parsable invoke element"
)

var (
	dsmlBlockRe       = regexp.MustCompile(`(?s)\n?` + dsmlCallsOpen + `(.*?)` + dsmlCallsClose + `\n?`)
	dsmlCallsOpenRe   = regexp.MustCompile(dsmlCallsOpen)
	dsmlCallsCloseRe  = regexp.MustCompile(dsmlCallsClose)
	dsmlInvokeOpenRe  = regexp.MustCompile(dsmlInvokeHead + `"[^"]+"\s*>`)
	dsmlInvokeHeadRe  = regexp.MustCompile(dsmlInvokeHead)
	dsmlInvokeCloseRe = regexp.MustCompile(dsmlInvokeClose)
	dsmlInvokeRe      = regexp.MustCompile(`(?s)` + dsmlInvokeHead + `"([^"]+)"\s*>(.*?)` + dsmlInvokeClose)
	// dsmlPartialTagRe matches an unterminated DSML tag fragment as a second,
	// regex-based opinion behind dsmlTagPrefixLen: it catches tag shapes whose
	// namespace word is complete but whose element name has not arrived yet.
	dsmlPartialTagRe = regexp.MustCompile(`^</?` + dsmlBars + `(?:[A-Za-z]{0,4}|DSML` + dsmlBars + `[^>\n]*)$`)

	dsmlParamHeadRe = regexp.MustCompile(dsmlParamHead)
	// dsmlResidualRe matches any leftover DSML tag fragment, including a
	// half-written tag at the end of a truncated stream. It is used by the
	// report inspector to measure how much prose survives once markup is
	// stripped.
	dsmlResidualRe = regexp.MustCompile(`</?` + dsmlBars + `DSML` + dsmlBars + `[^>\n]*>?`)
	dsmlParamRe    = regexp.MustCompile(`(?s)` + dsmlParamHead + `"([^"]+)"(?:\s+string="(true|false)")?\s*>(.*?)` + dsmlParamClose)
)

// maxDSMLTagLen bounds how many bytes the streaming filter may hold back
// while waiting for a tag to complete. No DSML tag comes close to this.
const maxDSMLTagLen = 64

// dsmlBarRune is one U+FF5C bar. The streaming filter compares bytes with it
// on purpose: a delta can split this three-byte rune, and a rune-oriented
// matcher reads the fragment as replacement characters and lets the markup
// leak to the view.
const dsmlBarRune = "\uFF5C"

// partialBarPrefix reports whether s is the beginning of a U+FF5C bar.
func partialBarPrefix(s string) bool {
	return len(s) < len(dsmlBarRune) && strings.HasPrefix(dsmlBarRune, s)
}

// dsmlTagPrefixLen reports the byte length of the tail starting at s when it
// could still grow into DSML markup, and 0 when it cannot. It accepts any bar
// count, any separator whitespace and any split point, so it covers the whole
// grammar instead of an enumeration of common spellings.
func dsmlTagPrefixLen(s string) int {
	if s == "" || s[0] != '<' {
		return 0
	}
	if strings.IndexByte(s, '>') >= 0 {
		// A terminated tag: scanOpen has already had its chance to match it, so
		// holding it back would only delay legitimate output.
		return 0
	}
	i := 1
	if i < len(s) && s[i] == '/' {
		i++
	}
	if i >= len(s) {
		return len(s)
	}
	bars := 0
	for i < len(s) {
		if strings.HasPrefix(s[i:], dsmlBarRune) {
			i += len(dsmlBarRune)
			bars++
			continue
		}
		if partialBarPrefix(s[i:]) {
			return len(s)
		}
		break
	}
	if bars == 0 {
		return 0
	}
	rest := s[i:]
	const namespace = "DSML"
	for j := 0; j < len(namespace); j++ {
		if j >= len(rest) {
			return len(s)
		}
		if rest[j] != namespace[j] {
			return 0
		}
	}
	return len(s)
}

// heldTailLen returns how many trailing bytes of pending must be withheld
// from the live stream because they may still complete into DSML markup.
func heldTailLen(pending string) int {
	if len(pending) > maxDSMLTagLen {
		pending = pending[len(pending)-maxDSMLTagLen:]
	}
	start := strings.LastIndexByte(pending, '<')
	if start < 0 {
		return 0
	}
	tail := pending[start:]
	if dsmlTagPrefixLen(tail) > 0 || dsmlPartialTagRe.MatchString(tail) {
		return len(tail)
	}
	return 0
}

// dsmlTokenFilter strips DSML markup from streaming content deltas in real
// time so that OnToken callbacks never show leaked markup to the user. It
// tracks block boundaries statefully across chunk boundaries, holds back a
// bounded tail that may still be an incomplete tag. Bytes that turn out not
// to be markup are released on the next non-tag delta; a stream that ends on
// a partial tag simply withholds that fragment (at most maxDSMLTagLen bytes)
// from the live view, while the message content keeps every byte for
// post-stream recovery.
//
// Suppression is keyed on whichever element opened the block: a full
// calls-wrapper closes on the block close tag, while a bare invoke (no
// wrapper) closes on the invoke close tag, so a missing wrapper cannot
// swallow the rest of the answer.
type dsmlTokenFilter struct {
	inBlock bool
	bare    bool // opened by a bare invoke element rather than a calls block
	pending string
}

// filterToken processes one content delta and returns the portion that is
// safe to forward to OnToken. An empty result means the delta was fully
// suppressed or held back.
func (f *dsmlTokenFilter) filterToken(token string) string {
	f.pending += token
	var out strings.Builder

	for {
		if f.inBlock {
			closeRe := dsmlCallsCloseRe
			if f.bare {
				closeRe = dsmlInvokeCloseRe
			}
			if loc := closeRe.FindStringIndex(f.pending); loc != nil {
				f.inBlock, f.bare = false, false
				f.pending = f.pending[loc[1]:]
				continue
			}
			// Still inside the block: nothing here may reach the view. Keep
			// only a bounded tail so a split close tag is still detectable and
			// a long leaked block cannot grow the buffer without limit.
			if len(f.pending) > maxDSMLTagLen {
				f.pending = f.pending[len(f.pending)-maxDSMLTagLen:]
			}
			return out.String()
		}

		openAt, openLen, bare := f.scanOpen()
		if openAt >= 0 {
			out.WriteString(f.pending[:openAt])
			f.inBlock, f.bare = true, bare
			f.pending = f.pending[openAt+openLen:]
			continue
		}

		hold := heldTailLen(f.pending)
		out.WriteString(f.pending[:len(f.pending)-hold])
		f.pending = f.pending[len(f.pending)-hold:]
		return out.String()
	}
}

// scanOpen locates the earliest DSML element that starts suppression.
// Returns the byte offset, the length of the consumed opener, and whether
// the opener was a bare invoke rather than a calls wrapper.
func (f *dsmlTokenFilter) scanOpen() (at, length int, bare bool) {
	bestAt, bestLen, bestBare := -1, 0, false
	if loc := dsmlCallsOpenRe.FindStringIndex(f.pending); loc != nil {
		bestAt, bestLen = loc[0], loc[1]-loc[0]
	}
	invokeRe := dsmlInvokeOpenRe
	if loc := invokeRe.FindStringIndex(f.pending); loc != nil && (bestAt < 0 || loc[0] < bestAt) {
		bestAt, bestLen, bestBare = loc[0], loc[1]-loc[0], true
	}
	return bestAt, bestLen, bestBare
}

// parseDSMLToolCalls detects DSML tool-call markup leaked into content and
// converts it to proper tool calls. It returns the cleaned content, the
// recovered calls, rejection reasons for markup that could not be turned
// into a dispatchable call, and whether any markup was found.
//
// Markup inside a fenced code block or a blockquote is a quoted example, not
// a call: it stays prose, so answering "what does DSML look like?" cannot
// execute the sample. This mirrors the attribute dialect's gate. Every
// unquoted block is recovered - multiple blocks included - and the prose
// around recovered markup is preserved rather than truncated.
func parseDSMLToolCalls(content string, dsmlSeq *int) (string, []types.ToolCall, []string, bool) {
	if !strings.Contains(content, "DSML") {
		return content, nil, nil, false
	}

	protected := attrProtectedSpans(content)

	var calls []types.ToolCall
	var kept strings.Builder
	found := false

	base := 0 // absolute offset of rest within content
	rest := content
	for {
		loc := dsmlBlockRe.FindStringIndex(rest)
		if loc == nil {
			kept.WriteString(rest)
			break
		}
		if attrInSpan(base+loc[0], protected) {
			// Quoted, fenced or blockquoted example: keep verbatim, scan on.
			kept.WriteString(rest[:loc[1]])
			base += loc[1]
			rest = rest[loc[1]:]
			continue
		}
		found = true
		kept.WriteString(rest[:loc[0]])
		calls = append(calls, parseDSMLInvokes(rest[loc[0]:loc[1]], dsmlSeq)...)
		base += loc[1]
		rest = rest[loc[1]:]
	}

	if found && len(calls) == 0 {
		// Markup is present but nothing in it can be dispatched. Keep the
		// original content so the report guard can still flag it, and say so:
		// the caller fails the turn rather than presenting unexecuted markup
		// as the answer.
		return content, nil, []string{rejectDSMLNoInvoke}, true
	}
	if found {
		return strings.TrimSpace(kept.String()), calls, nil, true
	}

	// No complete block: the wrapper was never emitted, or the stream was cut
	// off mid-markup. Complete invoke elements are still salvaged and prose on
	// both sides is preserved; residual tag fragments are stripped instead of
	// truncating everything from the first element on.
	cut := dsmlMarkupStart(content)
	if cut < 0 || attrInSpan(cut, protected) {
		return content, nil, nil, false
	}
	calls = parseDSMLInvokes(content[cut:], dsmlSeq)
	if len(dsmlInvokeHeadRe.FindAllStringIndex(content, -1)) > len(dsmlInvokeRe.FindAllStringIndex(content, -1)) {
		// At least one invoke has no closing tag: the markup was cut off and
		// the tail of it is junk, including any bare parameter value. Keep the
		// prose that precedes it and nothing else.
		return strings.TrimSpace(content[:cut]), calls, nil, true
	}
	cleaned := dsmlInvokeRe.ReplaceAllString(content, "")
	cleaned = dsmlResidualRe.ReplaceAllString(cleaned, "")
	return strings.TrimSpace(cleaned), calls, nil, true
}

// dsmlMarkupStart returns the offset of the first DSML element in content,
// or -1 when there is none. It only matches real tag structure, never the
// bare word "DSML", so prose that merely discusses the dialect is left
// alone.
func dsmlMarkupStart(content string) int {
	start := -1
	for _, re := range []*regexp.Regexp{dsmlCallsOpenRe, dsmlInvokeHeadRe, dsmlParamHeadRe} {
		if loc := re.FindStringIndex(content); loc != nil && (start < 0 || loc[0] < start) {
			start = loc[0]
		}
	}
	return start
}

func parseDSMLInvokes(block string, dsmlSeq *int) []types.ToolCall {
	var calls []types.ToolCall
	for _, m := range dsmlInvokeRe.FindAllStringSubmatch(block, -1) {
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

		id := fmt.Sprintf("dsml_%d", *dsmlSeq)
		*dsmlSeq++

		calls = append(calls, types.ToolCall{
			ID:    id,
			Index: len(calls),
			Type:  "function",
			Function: types.ToolCallFn{
				Name:      name,
				Arguments: string(argsJSON),
			},
		})
	}
	return calls
}
