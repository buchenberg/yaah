package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

// feedIn feeds content to the detector in deltas of deltaBytes so tests can
// exercise tokens split across deltas.
func feedIn(d *repetitionDetector, content string, deltaBytes int) (repetitionLoop, bool) {
	if deltaBytes <= 0 || deltaBytes > len(content) {
		deltaBytes = len(content)
	}
	for i := 0; i < len(content); i += deltaBytes {
		end := i + deltaBytes
		if end > len(content) {
			end = len(content)
		}
		if loop, tripped := d.feed(content[i:end]); tripped {
			return loop, true
		}
	}
	return repetitionLoop{}, false
}

// TestRepetitionDetector_trips pins both trip rules and the reported offset. The
// offset is the contract that matters most: it is what the caller truncates at,
// so the kept text must end before the loop and never mid-loop.
func TestRepetitionDetector_trips(t *testing.T) {
	const preamble = "Here are the results:\n"

	tests := []struct {
		name       string
		content    string
		wantToken  string
		wantPeriod int
		wantCount  int
		wantKeep   int
	}{
		{
			name:       "same token run",
			content:    strings.Repeat("GO ", repetitionRunTokens),
			wantToken:  "GO",
			wantPeriod: 1,
			wantCount:  repetitionRunTokens,
			wantKeep:   0,
		},
		{
			name:       "same token run after preamble",
			content:    preamble + strings.Repeat("GO ", repetitionRunTokens),
			wantToken:  "GO",
			wantPeriod: 1,
			wantCount:  repetitionRunTokens,
			wantKeep:   len(preamble),
		},
		{
			name:       "two-token shingle",
			content:    preamble + strings.Repeat("step one ", repetitionShingleReps),
			wantToken:  "step one",
			wantPeriod: 2,
			wantCount:  repetitionShingleReps,
			wantKeep:   len(preamble),
		},
		{
			name:       "three-token shingle",
			content:    preamble + strings.Repeat("a b c ", repetitionShingleReps),
			wantToken:  "a b c",
			wantPeriod: 3,
			wantCount:  repetitionShingleReps,
			wantKeep:   len(preamble),
		},
		{
			name:       "eight-token shingle",
			content:    preamble + strings.Repeat("one two three four five six seven eight ", repetitionShingleReps),
			wantToken:  "one two three four five six seven eight",
			wantPeriod: 8,
			wantCount:  repetitionShingleReps,
			wantKeep:   len(preamble),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := repetitionDetector{minSpan: 1}
			loop, tripped := feedIn(&d, tc.content, 0)
			if !tripped {
				t.Fatalf("no trip on %q...", tc.content[:min(len(tc.content), 40)])
			}
			if loop.Token != tc.wantToken {
				t.Errorf("token = %q, want %q", loop.Token, tc.wantToken)
			}
			if loop.Period != tc.wantPeriod {
				t.Errorf("period = %d, want %d", loop.Period, tc.wantPeriod)
			}
			if loop.Count < tc.wantCount {
				t.Errorf("count = %d, want >= %d", loop.Count, tc.wantCount)
			}
			if loop.KeepBytes != tc.wantKeep {
				t.Errorf("keepBytes = %d, want %d", loop.KeepBytes, tc.wantKeep)
			}
			// The kept prefix must not run into the loop.
			kept := truncateAtRuneBoundary(tc.content, loop.KeepBytes)
			if kept != tc.content[:tc.wantKeep] {
				t.Errorf("kept %q, want the text before the loop %q", kept, tc.content[:tc.wantKeep])
			}
		})
	}
}

// TestRepetitionDetector_boundaries pins the exact thresholds: one repetition
// below each bar must survive, the bar itself must trip. Without this the
// constants could drift by an off-by-one and nobody would notice.
func TestRepetitionDetector_boundaries(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"one under the token run", strings.Repeat("GO ", repetitionRunTokens-1), false},
		{"at the token run", strings.Repeat("GO ", repetitionRunTokens), true},
		{"one under the shingle run", strings.Repeat("step one ", repetitionShingleReps-1), false},
		{"at the shingle run", strings.Repeat("step one ", repetitionShingleReps), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := repetitionDetector{minSpan: 1}
			if _, tripped := d.feed(tc.content); tripped != tc.want {
				t.Errorf("tripped = %v, want %v for %d tokens", tripped, tc.want, len(strings.Fields(tc.content)))
			}
		})
	}
}

// TestRepetitionDetector_ignoresLegitimate is the false-positive guard: every
// case here is output a model legitimately produces, and truncating any of them
// would corrupt a correct answer.
func TestRepetitionDetector_ignoresLegitimate(t *testing.T) {
	tableOfRows := "| name | status | note |\n| --- | --- | --- |\n" +
		strings.Repeat("| alpha | ok | same row |\n", 20)

	var numberedList strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&numberedList, "%d. step %d does the same thing\n", i, i)
	}

	tests := []struct {
		name    string
		content string
	}{
		{"table of 20 identical rows", tableOfRows},
		{"numbered list", numberedList.String()},
		{"prose with a repeated word", strings.Repeat("the the the cat sat on the mat. ", 4) +
			"That is all. The end. Nothing more to say here.\n"},
		{
			// Guards the primitive-period gate: a 150-token column of the
			// same token is legitimate (a matrix of zeros, a table of
			// dashes) and must survive even though it is periodic.
			name:    "column of 150 identical tokens",
			content: strings.Repeat("--- ", 150),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := repetitionDetector{minSpan: 1}
			if loop, tripped := feedIn(&d, tc.content, 0); tripped {
				t.Fatalf("false positive: token %q period %d count %d keep %d",
					loop.Token, loop.Period, loop.Count, loop.KeepBytes)
			}
			if got := len(d.pending); got > repetitionFragmentBytes {
				t.Errorf("pending fragment = %d bytes, want <= %d (the buffer must stay bounded)",
					got, repetitionFragmentBytes)
			}
		})
	}
}

// TestRepetitionDetector_splitTokenAcrossDeltas pins the buffering rule: a
// provider splits tokens wherever it likes, and a detector that compares
// fragments instead of tokens never sees the loop.
func TestRepetitionDetector_splitTokenAcrossDeltas(t *testing.T) {
	content := "Working on it:\n" + strings.Repeat("GO ", 1200)
	for _, deltaBytes := range []int{1, 2, 3, 5, 7} {
		t.Run(fmt.Sprintf("%d-byte deltas", deltaBytes), func(t *testing.T) {
			d := repetitionDetector{minSpan: 1}
			loop, tripped := feedIn(&d, content, deltaBytes)
			if !tripped {
				t.Fatal("no trip when the loop is split across deltas")
			}
			if loop.Token != "GO" || loop.Period != 1 {
				t.Errorf("token = %q period = %d, want GO / 1", loop.Token, loop.Period)
			}
			if loop.Count < repetitionRunTokens {
				t.Errorf("count = %d, want >= %d", loop.Count, repetitionRunTokens)
			}
			if want := len("Working on it:\n"); loop.KeepBytes != want {
				t.Errorf("keepBytes = %d, want %d", loop.KeepBytes, want)
			}
		})
	}
}

// TestRepetitionDetector_tripsOnce pins the latch: a tripped detector reports
// nothing further, so a caller that keeps feeding cannot see a second loop.
func TestRepetitionDetector_tripsOnce(t *testing.T) {
	d := repetitionDetector{minSpan: 1}
	content := strings.Repeat("GO ", 1200)
	if _, tripped := d.feed(content); !tripped {
		t.Fatal("expected a trip")
	}
	if _, tripped := d.feed("GO GO GO "); tripped {
		t.Error("a tripped detector must stay quiet")
	}
}

// TestDegenerateRepetitionError_namesTokenAndCount pins the human-facing
// contract: the turn error must say what repeated and how often, and the type
// must be reachable both by errors.Is and errors.As.
func TestDegenerateRepetitionError_namesTokenAndCount(t *testing.T) {
	err := error(&DegenerateRepetitionError{Token: "GO", Period: 1, Count: 200, KeptBytes: 42})
	if !errors.Is(err, ErrDegenerateRepetition) {
		t.Error("errors.Is(err, ErrDegenerateRepetition) = false, want true")
	}
	var typed *DegenerateRepetitionError
	if !errors.As(err, &typed) {
		t.Fatal("errors.As(*DegenerateRepetitionError) = false, want true")
	}
	for _, want := range []string{`"GO"`, "200 times", "42 bytes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q must contain %q", err.Error(), want)
		}
	}

	shingle := error(&DegenerateRepetitionError{Token: "step one", Period: 2, Count: 64})
	if !strings.Contains(shingle.Error(), "2-token pattern") {
		t.Errorf("message %q must name the pattern width", shingle.Error())
	}
}

// loopingStreamProvider streams an optional prefix and then a fixed looping
// token, one chunk per token, so the detector sees exactly what a looping model
// produces.
type loopingStreamProvider struct {
	prefix string
	token  string
	count  int
	calls  int
}

func (p *loopingStreamProvider) Send(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
	return nil, errors.New("streaming only")
}

func (p *loopingStreamProvider) SendStream(ctx context.Context, _ types.ChatRequest) (<-chan types.StreamChunk, <-chan error) {
	p.calls++
	chunks := make(chan types.StreamChunk, 4)
	errs := make(chan error, 1)
	go func() {
		defer close(chunks)
		defer close(errs)
		if p.prefix != "" {
			select {
			case chunks <- types.StreamChunk{Choices: []types.StreamChoice{{
				Delta: types.StreamDelta{Content: p.prefix},
			}}}:
			case <-ctx.Done():
				return
			}
		}
		for i := 0; i < p.count; i++ {
			select {
			case chunks <- types.StreamChunk{Choices: []types.StreamChoice{{
				Delta: types.StreamDelta{Content: p.token},
			}}}:
			case <-ctx.Done():
				return
			}
		}
		select {
		case chunks <- types.StreamChunk{Choices: []types.StreamChoice{{
			FinishReason: strPtr("stop"),
		}}}:
		case <-ctx.Done():
		}
	}()
	return chunks, errs
}

// TestRunStream_degenerateRepetitionAborts pins the stream-level contract: the
// accumulating content is truncated where the loop started and the looping text
// is not handed back as the model's answer.
func TestRunStream_degenerateRepetitionAborts(t *testing.T) {
	const preamble = "Working on it:\n"
	p := &loopingStreamProvider{prefix: preamble, token: "GO ", count: 1200}
	c := newStreamingClient(p)

	msg, finishReason, _, _, err := c.runStream(context.Background(), p, types.ChatRequest{Model: "test-model"})
	if err == nil {
		t.Fatal("expected a degenerate-repetition error")
	}
	if !errors.Is(err, ErrDegenerateRepetition) {
		t.Errorf("error = %v, want ErrDegenerateRepetition", err)
	}
	var typed *DegenerateRepetitionError
	if !errors.As(err, &typed) {
		t.Fatalf("error %v does not unwrap to *DegenerateRepetitionError", err)
	}
	if typed.Token != "GO" {
		t.Errorf("repeated token = %q, want GO", typed.Token)
	}
	if typed.Count < repetitionRunTokens {
		t.Errorf("count = %d, want >= %d", typed.Count, repetitionRunTokens)
	}
	if finishReason != "" {
		t.Errorf("finish reason = %q, want empty (the stream was aborted)", finishReason)
	}
	if strings.Contains(msg.Content, "GO") {
		t.Errorf("content %q still contains the loop", msg.Content)
	}
	if msg.Content != preamble {
		t.Errorf("content = %q, want the text before the loop %q", msg.Content, preamble)
	}
}

// TestCall_degenerateRepetitionSurfacesError: with no fallback provider there is
// nothing that can break the loop, so the typed error surfaces on the FIRST
// attempt. Replaying on the same provider would burn the same wall clock again.
func TestCall_degenerateRepetitionSurfacesError(t *testing.T) {
	p := &loopingStreamProvider{token: "GO ", count: 1200}
	c := newStreamingClient(p)
	c.MaxRetries = 2

	res, err := c.Call(context.Background(), types.ChatRequest{Model: "test-model"})
	if !errors.Is(err, ErrDegenerateRepetition) {
		t.Fatalf("error = %v, want ErrDegenerateRepetition", err)
	}
	if p.calls != 1 {
		t.Errorf("provider calls = %d, want 1 (no same-provider replay)", p.calls)
	}
	if c.replayCount != 0 {
		t.Errorf("replayCount = %d, want 0 (the empty-stream ladder must not run)", c.replayCount)
	}
	if strings.Contains(res.Message.Content, "GO") {
		t.Errorf("content %q still contains the loop", res.Message.Content)
	}
	// The count is the number of repetitions *observed*, not the threshold:
	// the span floor means the trip can land well past the count bar, and the
	// message says "at least" because the true loop is always longer.
	var typed *DegenerateRepetitionError
	if !errors.As(err, &typed) {
		t.Fatalf("error %v is not a *DegenerateRepetitionError", err)
	}
	if typed.Count < repetitionRunTokens {
		t.Errorf("count = %d, want >= %d", typed.Count, repetitionRunTokens)
	}
	if !strings.Contains(err.Error(), "at least") {
		t.Errorf("turn error %q must say the count is a floor", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d times", typed.Count)) {
		t.Errorf("turn error %q must name the repetition count %d", err, typed.Count)
	}
}

// TestCall_degenerateRepetitionSwapsToFallback pins the one recovery that can
// work: a different provider breaks the loop.
func TestCall_degenerateRepetitionSwapsToFallback(t *testing.T) {
	primary := &loopingStreamProvider{token: "GO ", count: 1200}
	fallback := &abruptStreamProvider{}
	c := newStreamingClient(primary)
	c.FallbackProvider = fallback
	c.FallbackModel = "fallback-model"

	res, err := c.Call(context.Background(), types.ChatRequest{Model: "test-model"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Message.Content != "recovered" {
		t.Errorf("content = %q, want recovered from the fallback provider", res.Message.Content)
	}
	if c.Provider != Provider(fallback) {
		t.Error("primary did not rotate to the fallback")
	}
	if c.Model != "fallback-model" {
		t.Errorf("model = %q, want fallback-model", c.Model)
	}
	if primary.calls != 1 {
		t.Errorf("primary calls = %d, want 1", primary.calls)
	}
}

// TestCall_degenerateRepetitionSwapsOnce pins the bound: a fallback that loops
// too surfaces the typed error instead of swapping back and forth.
func TestCall_degenerateRepetitionSwapsOnce(t *testing.T) {
	primary := &loopingStreamProvider{token: "GO ", count: 1200}
	fallback := &loopingStreamProvider{token: "GO ", count: 1200}
	c := newStreamingClient(primary)
	c.FallbackProvider = fallback
	c.FallbackModel = "fallback-model"

	_, err := c.Call(context.Background(), types.ChatRequest{Model: "test-model"})
	if !errors.Is(err, ErrDegenerateRepetition) {
		t.Fatalf("error = %v, want ErrDegenerateRepetition", err)
	}
	if primary.calls != 1 || fallback.calls != 1 {
		t.Errorf("provider calls = primary %d / fallback %d, want exactly one each",
			primary.calls, fallback.calls)
	}
}
