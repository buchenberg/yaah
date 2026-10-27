package tools

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Tool-result policy: truncation and multi-step outcomes live here and
// nowhere else.
//
// Two invariants govern every model-facing result:
//
//  1. A nil error from Execute means the operation succeeded, full stop.
//     A partially applied operation returns a non-nil error. Prose that
//     describes a failure must never ride inside a successful result.
//
//  2. Error detail is never truncated. multiResult renders every failure
//     before the success detail, so a cap (this one or the agent loop's
//     outer safety cap) can only ever eat success detail.
//
// The agent loop applies a second, larger byte/line cap with spillover
// (internal/agent/agent_truncation.go). That cap exists for context-window
// safety; it is not a licence for tools to invent their own limits. Tools
// return at most toolResultMaxBytes of success detail, which is below the
// loop cap, and errors are passed through untruncated.
//
// Boundary case: when an operation fails after producing a partial result —
// the report lane's retainedNote caveat naming a stranded worktree is the
// reason — the partial travels inside the error text, because the loop
// delivers error text verbatim and discards the result. The partial is
// success-detail content: foldRetainedPartial bounds both halves (body
// head-first, caveat tail-first) to keep the whole error finite while the
// error's own diagnostic stays untruncated.
const (
	// toolResultMaxBytes caps the success detail a single tool call returns.
	toolResultMaxBytes = 8192
	// commandOutputMaxBytes caps raw command output (stdout/stderr) returned
	// by shell tools. The agent loop's outer cap may cut this further and
	// spill the full text to disk.
	commandOutputMaxBytes = 1 << 20 // 1 MiB
	// truncationMarker is the only marker tools append when they cap
	// success output.
	truncationMarker = "\n...[truncated]..."
	// tailTruncationMarker prefixes text kept from the END of an over-long
	// payload: what was lost came before the kept text, not after. Both
	// markers are the only ones tools append when they cap output.
	tailTruncationMarker = "...[earlier text truncated]..."
)

// truncateAt caps s at max bytes on a UTF-8 rune boundary and appends
// truncationMarker when it cuts. max <= 0 means no cap.
func truncateAt(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + truncationMarker
}

// truncateResult caps s at toolResultMaxBytes on a UTF-8 rune boundary and
// appends truncationMarker when it cuts.
func truncateResult(s string) string {
	return truncateAt(s, toolResultMaxBytes)
}

// keepTail caps s at max bytes on a UTF-8 rune boundary, keeping the LAST
// max bytes and prefixing tailTruncationMarker when it cuts. Teardown notes
// end with the worktree path they are reporting, so a note that must fit a
// budget keeps its tail, not its head.
func keepTail(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := len(s) - max
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return tailTruncationMarker + s[cut:]
}

// truncateOutput caps raw command output at commandOutputMaxBytes on a UTF-8
// rune boundary and appends truncationMarker when it cuts.
func truncateOutput(b []byte) []byte {
	if len(b) <= commandOutputMaxBytes {
		return b
	}
	cut := commandOutputMaxBytes
	for cut > 0 && !utf8.RuneStart(b[cut]) {
		cut--
	}
	return append(b[:cut:cut], truncationMarker...)
}

// multiResult accumulates the outcome of a tool operation that can partially
// apply: successes in done, failures in failed.
//
// It is deliberately tiny and not a general result builder. Its job is to make
// the two rungs of the policy above hard to get wrong: Report returns a
// non-nil error whenever any step failed, and renders failures ahead of
// success detail that may be capped.
type multiResult struct {
	done   []string
	failed []string
}

// donef records a successful step.
func (r *multiResult) donef(format string, args ...any) {
	r.done = append(r.done, fmt.Sprintf(format, args...))
}

// failf records a failed step.
func (r *multiResult) failf(format string, args ...any) {
	r.failed = append(r.failed, fmt.Sprintf(format, args...))
}

// failedCount reports how many steps failed.
func (r *multiResult) failedCount() int { return len(r.failed) }

// Report renders headline plus the recorded steps. Every failure is rendered
// first, complete and untruncated; the success detail follows and is capped by
// truncateResult. When any step failed Report also returns a non-nil error
// carrying the same report, because a partial application is not a success.
func (r *multiResult) Report(headline string) (string, error) {
	var b strings.Builder
	b.WriteString(headline)

	if len(r.failed) > 0 {
		fmt.Fprintf(&b, "\nFAILED (%d):", len(r.failed))
		for _, f := range r.failed {
			b.WriteString("\n  ")
			b.WriteString(f)
		}
	}

	if len(r.done) > 0 {
		var success strings.Builder
		for _, d := range r.done {
			success.WriteString("\n  ")
			success.WriteString(d)
		}
		b.WriteString(truncateResult(success.String()))
	}

	report := b.String()
	if len(r.failed) > 0 {
		return report, errors.New(report)
	}
	return report, nil
}
