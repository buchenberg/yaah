package llm

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Degenerate repetition guard.
//
// A model that loses the plot and emits "GO GO GO ..." for three hundred
// thousand tokens reads as a perfectly successful completion: the stream ends
// with a finish_reason, the content is non-empty, and the only pre-existing
// guard (isDegenerateStream in client.go) fires on an EMPTY response only. The
// cost is not theoretical — one real session stored a single 2.78 MB assistant
// message after ~40 minutes of wall clock spent streaming it.
//
// repetitionDetector is the incremental guard for that failure. It is fed every
// streamed content delta, tokenizes the content on whitespace, and reports the
// moment the token stream collapses into a loop, together with the offset of
// the text that preceded the loop so the caller can keep that and discard the
// rest.
//
// # Thresholds
//
// Both rules sit deliberately far above anything ordinary prose produces,
// because a false positive truncates a real answer:
//
//   - repetitionRunTokens (200): the same token repeated consecutively. Lists,
//     tables and data dumps legitimately repeat one token a few dozen times
//     ("|", "---", "0"), so the bar is 200 in a row — tens of KB of one token
//     with nothing between the occurrences.
//   - repetitionShingleReps (64): a 2..repetitionMaxPeriod-token shingle
//     repeated consecutively. A multi-token pattern repeating 64 times is not
//     something a model writes by accident; 128 tokens of the same 2-token
//     unit is nobody's deliberate output. (A 64-row table of identical rows
//     would trip the period for that row's width — accepted, that output is
//     degenerate whatever produced it.)
//   - repetitionMaxPeriod (8): the longest shingle considered. Detection is
//     already unambiguous at 64 repetitions of 8 tokens, and a longer period
//     costs proportional look-back for no extra signal.
//
// Period 1 is rule (a) with the 200 threshold rather than rule (b) with 64: a
// column of identical tokens is legitimate output far more often than a
// periodic multi-token loop, so a single-token run needs the higher bar. Both
// rules share one code path (see repetitionTripRepeats).
const (
	repetitionRunTokens     = 200
	repetitionShingleReps   = 64
	repetitionMaxPeriod     = 8
	repetitionRing          = 16 // > repetitionMaxPeriod, so a compared cell is never yet overwritten
	repetitionTokenBytes    = 128
	repetitionFragmentBytes = 1024
	repetitionDescribeBytes = 160
)

// ErrDegenerateRepetition is the sentinel for a model trapped in a repetition
// loop. It is returned by Client.Call wrapped in a *DegenerateRepetitionError
// that names the repeated token and the repetition count.
var ErrDegenerateRepetition = errors.New("degenerate repetition in model output")

// DegenerateRepetitionError reports a repetition loop that aborted a streamed
// response. It unwraps to ErrDegenerateRepetition, so callers can either
// errors.Is the sentinel or errors.As the detail.
type DegenerateRepetitionError struct {
	// Token is the repeated unit, rendered for humans: the token itself for
	// a single-token run, or the shingle's tokens joined by single spaces.
	Token string
	// Period is the number of tokens in the repeated unit (1 for a plain
	// token run).
	Period int
	// Count is how many times the unit repeated consecutively before the
	// stream was aborted.
	Count int
	// KeptBytes is how many bytes of the response preceded the loop and
	// were kept as partial output.
	KeptBytes int
}

func (e *DegenerateRepetitionError) Error() string {
	unit := fmt.Sprintf("token %q", e.Token)
	if e.Period > 1 {
		unit = fmt.Sprintf("%d-token pattern %q", e.Period, e.Token)
	}
	return fmt.Sprintf(
		"degenerate repetition: the model repeated the same %s %d times in a row; the stream was aborted at %d bytes of output and the looping text was discarded. Replaying the same request against the same model reproduces the loop, so change the prompt or temperature, or switch model/provider",
		unit, e.Count, e.KeptBytes)
}

// Unwrap makes errors.Is(err, ErrDegenerateRepetition) work.
func (e *DegenerateRepetitionError) Unwrap() error { return ErrDegenerateRepetition }

// repetitionLoop describes a detected loop: where the clean text ends and what
// the model got stuck on.
type repetitionLoop struct {
	// KeepBytes is how many bytes of the accumulated content precede the
	// loop. Truncating there drops the loop without cutting it in half.
	KeepBytes int
	// Token is the repeated unit, rendered for the error message.
	Token string
	// Count is how many times the unit repeated consecutively.
	Count int
	// Period is the number of tokens in the unit.
	Period int
}

// repetitionTripRepeats returns how many consecutive repetitions of a period-p
// shingle trip the guard. Period 1 needs repetitionRunTokens (rule (a)); every
// longer period needs repetitionShingleReps (rule (b)).
func repetitionTripRepeats(period int) int {
	if period == 1 {
		return repetitionRunTokens
	}
	return repetitionShingleReps
}

// repetitionSubPeriods lists, for each period, its proper divisors. A
// period-p run that is really a run of a smaller period q must be counted under
// q, not under p: all identical tokens are period 1, and without this gate a
// 127-token identical run would trip as a "period 2" loop long before rule (a)'s
// 200-token bar. Attributing the run to the primitive period is safe because
// the smaller period's threshold is smaller and therefore reaches it first.
// Proper divisors are sufficient: a shingle that is both p- and q-periodic is
// gcd(p, q)-periodic over the tens of tokens such a run spans.
var repetitionSubPeriods = [repetitionMaxPeriod + 1][]int{
	2: {1},
	3: {1},
	4: {1, 2},
	5: {1},
	6: {1, 2, 3},
	7: {1},
	8: {1, 2, 4},
}

// repetitionCell is one remembered token: its byte offset in the accumulated
// content, its full length, and at most repetitionTokenBytes of its bytes. buf
// is reused in place as the ring wraps, so tokenizing an ordinary response
// allocates nothing after the first few tokens.
type repetitionCell struct {
	off    int
	length int
	buf    []byte
}

// repetitionDetector watches a streamed content accumulator for a runaway
// token loop. The zero value is ready to use.
//
// Memory is O(repetitionRing*repetitionTokenBytes + repetitionFragmentBytes)
// regardless of the response length: only the last repetitionRing tokens and
// the incomplete trailing fragment are retained, so a loop is caught long
// before the accumulator grows, and text with no whitespace at all (CJK prose,
// minified code) cannot grow the buffer with the response either.
type repetitionDetector struct {
	// pending buffers the trailing token fragment: a token split across two
	// deltas must be compared whole, or a looping "GO" never repeats.
	pending []byte
	// fed counts content bytes fed so far, so a token's offset in the
	// accumulated content is fed-len(pending)+i.
	fed int
	// ring holds the last repetitionRing completed tokens and their offsets.
	ring [repetitionRing]repetitionCell
	// tokens counts completed tokens (monotonic); it indexes ring.
	tokens int
	// counts[p] is the number of consecutive alignment steps for period p,
	// i.e. consecutive tokens equal to the token p positions earlier.
	counts [repetitionMaxPeriod + 1]int
	// starts[p] is the content offset where the current period-p run's
	// repeated span begins — the offset to keep when that period trips.
	starts [repetitionMaxPeriod + 1]int
	// tripped latches; a tripped detector ignores further input.
	tripped bool
}

// feed consumes one streamed content delta and reports a loop when it detects
// one. It must be called for every non-empty content delta, in order.
func (d *repetitionDetector) feed(delta string) (repetitionLoop, bool) {
	if d.tripped || delta == "" {
		return repetitionLoop{}, false
	}
	// Content offset of pending[0]: before the append, pending holds the
	// bytes of everything fed so far that has not yet become a token.
	base := d.fed - len(d.pending)
	d.pending = append(d.pending, delta...)
	d.fed += len(delta)

	from := 0
	for i := 0; i < len(d.pending); i++ {
		if isTokenSpace(d.pending[i]) {
			// Two whitespace bytes in a row produce no token: an "empty
			// token" repeating hundreds of times would trip the guard on
			// a run of spaces.
			if i > from {
				if loop, tripped := d.token(d.pending[from:i], base+from); tripped {
					d.tripped = true
					return loop, true
				}
			}
			from = i + 1
			continue
		}
		if i-from+1 >= repetitionFragmentBytes {
			// A whitespace-free run is cut into fixed-size tokens at the
			// bound instead of growing the buffer with the response. The
			// cut is deterministic (counted from the run's first byte), so
			// a periodic blob still produces equal chunks.
			if loop, tripped := d.token(d.pending[from:i+1], base+from); tripped {
				d.tripped = true
				return loop, true
			}
			from = i + 1
		}
	}

	// Retain the incomplete trailing fragment for the next delta. Bounded:
	// the loop above flushed anything the bound would have exceeded. The
	// from>0 guard keeps the common case — a delta that ends mid-token —
	// free of an in-place copy.
	if from > 0 {
		d.pending = append(d.pending[:0], d.pending[from:]...)
	}
	return repetitionLoop{}, false
}

// token records one complete token at content offset off and reports a loop
// when the token stream has collapsed into one.
func (d *repetitionDetector) token(tok []byte, off int) (repetitionLoop, bool) {
	n := d.tokens
	cell := &d.ring[n%repetitionRing]
	cell.off = off
	cell.length = len(tok)
	if len(tok) > repetitionTokenBytes {
		// The prefix plus the full length identifies a token: two tokens
		// sharing both are the same token for detection purposes, and
		// comparing 128 bytes keeps the per-token cost flat.
		tok = tok[:repetitionTokenBytes]
	}
	cell.buf = append(cell.buf[:0], tok...)
	d.tokens++

	for p := 1; p <= repetitionMaxPeriod; p++ {
		if n < p {
			break // no token p places back yet; counts start at zero
		}
		prev := &d.ring[(n-p)%repetitionRing]
		if !repetitionSameToken(cell, prev) {
			d.counts[p] = 0
			continue
		}
		if p > 1 && d.nonPrimitive(p) {
			d.counts[p] = 0
			continue
		}
		d.counts[p]++
		if d.counts[p] == 1 {
			// The run opens here, so the repeated span starts p tokens
			// back: that offset is the text to keep.
			d.starts[p] = prev.off
		}
		// k consecutive alignment steps span k+p tokens, which is
		// floor(k/p)+1 repetitions of the p-shingle. Trip when that
		// reaches the threshold for this period.
		if d.counts[p] >= (repetitionTripRepeats(p)-1)*p {
			d.tripped = true
			return repetitionLoop{
				KeepBytes: d.starts[p],
				Token:     d.unit(p),
				Count:     d.counts[p]/p + 1,
				Period:    p,
			}, true
		}
	}
	return repetitionLoop{}, false
}

// nonPrimitive reports that the period-p run in progress is really a run of a
// smaller period dividing p, which owns the report and has the lower threshold.
func (d *repetitionDetector) nonPrimitive(p int) bool {
	for _, q := range repetitionSubPeriods[p] {
		// counts[q] >= p means the q-periodic run already spans the last
		// p tokens, so the p-shingle carries no structure of its own.
		if d.counts[q] >= p {
			return true
		}
	}
	return false
}

// unit renders the repeated unit for the error message: the p tokens ending at
// the newest one, joined by single spaces and capped at repetitionDescribeBytes.
func (d *repetitionDetector) unit(p int) string {
	var b strings.Builder
	for i := p - 1; i >= 0; i-- {
		cell := &d.ring[(d.tokens-1-i)%repetitionRing]
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.Write(cell.buf)
		if cell.length > len(cell.buf) {
			b.WriteString("…")
		}
		if b.Len() >= repetitionDescribeBytes {
			break
		}
	}
	s := b.String()
	if len(s) > repetitionDescribeBytes {
		s = truncateAtRuneBoundary(s, repetitionDescribeBytes) + "…"
	}
	return s
}

// repetitionSameToken reports whether two remembered tokens are the same token.
func repetitionSameToken(a, b *repetitionCell) bool {
	return a.length == b.length && bytes.Equal(a.buf, b.buf)
}

// isTokenSpace reports whether b separates tokens. Only ASCII whitespace
// counts: it covers word-style tokenization of every language yaah is likely to
// see, and a byte test keeps the scan allocation-free. Multibyte spaces are then
// part of the token, which is harmless — the fragment bound keeps that bounded.
func isTokenSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}

// truncateAtRuneBoundary returns s shortened to at most n bytes without
// splitting a UTF-8 sequence in half.
func truncateAtRuneBoundary(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
