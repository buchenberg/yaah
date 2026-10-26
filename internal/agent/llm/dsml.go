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
	dsmlParamHeadRe   = regexp.MustCompile(dsmlParamHead)
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

// dsmlTagSkeletons is the literal form of every tag the grammar accepts,
// used by the streaming filter to decide whether a trailing fragment could
// still grow into DSML markup. The bar counts and spacing here are a
// superset of what the regexes accept in practice; a spelling outside this
// table may render live but is still recovered (or rejected) after the
// stream ends, which is the safe direction to be wrong in.
var dsmlTagSkeletons = func() []string {
	names := []string{
		"calls", "tool_calls", "function_calls",
		"invoke", "tool_call", "function_call",
		"parameter",
	}
	bars := []string{"\uFF5C", "\uFF5C\uFF5C", "\uFF5C\uFF5C\uFF5C"}
	spaces := []string{"", " ", "  "}
	out := make([]string, 0, 2*len(names)*len(bars)*len(spaces))
	for _, prefix := range []string{"<", "</"} {
		for _, bar := range bars {
			for _, space := range spaces {
				for _, name := range names {
					out = append(out, prefix+bar+"DSML"+bar+space+name)
				}
			}
		}
	}
	return out
}()

// couldBeDSMLTagStart reports whether s - the tail of the pending buffer,
// starting at a '<' - is a prefix of DSML markup, either because it has not
// grown to a full element name yet or because it already contains one.
func couldBeDSMLTagStart(s string) bool {
	for _, skel := range dsmlTagSkeletons {
		if strings.HasPrefix(s, skel) || strings.HasPrefix(skel, s) {
			return true
		}
	}
	return false
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
	if couldBeDSMLTagStart(tail) {
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
// Complete blocks and bare invoke elements (no calls wrapper) are both
// handled, as are truncated blocks where the stream was cut off mid-markup:
// there any complete invoke is salvaged and the remainder of the markup is
// stripped.
func parseDSMLToolCalls(content string, dsmlSeq *int) (string, []types.ToolCall, []string, bool) {
	if !strings.Contains(content, "DSML") {
		return content, nil, nil, false
	}

	if loc := dsmlBlockRe.FindStringIndex(content); loc != nil {
		calls := parseDSMLInvokes(content[loc[0]:loc[1]], dsmlSeq)
		if len(calls) == 0 {
			// The markup is present but nothing in it can be dispatched.
			// Keep it in the content and say so: the caller fails the turn.
			return content, nil, []string{rejectDSMLNoInvoke}, true
		}
		cleaned := strings.TrimSpace(dsmlBlockRe.ReplaceAllString(content, ""))
		return cleaned, calls, nil, true
	}

	// No complete block: either the wrapper was never emitted or the stream
	// was cut off mid-markup. Everything from the first element on is
	// markup-or-truncated-junk, so only the prose before it can be trusted;
	// complete invokes inside the remainder are salvaged.
	cut := dsmlMarkupStart(content)
	if cut < 0 {
		return content, nil, nil, false
	}
	prose := strings.TrimSpace(content[:cut])
	if calls := parseDSMLInvokes(content[cut:], dsmlSeq); len(calls) > 0 {
		return prose, calls, nil, true
	}
	// Opening markup with nothing salvageable after it. Rejecting would fail
	// benign truncated turns, so follow the long-standing truncation rule:
	// drop the markup, keep the prose.
	return prose, nil, nil, true
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
