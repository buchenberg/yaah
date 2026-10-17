package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/types"
)

func strPtr(s string) *string { return &s }

func TestCheckTruncatedStream_prematureClose(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		finishReason  string
		wantErr       bool
		wantPremature bool
	}{
		{"partial content no finish reason", "I'll do it now", "", true, true},
		{"empty content no finish reason", "", "", true, true},
		{"normal stop", "all done", "stop", false, false},
		{"empty with stop", "", "stop", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, _, err := checkTruncatedStream(tc.content, nil, tc.finishReason, "model-x", "", types.Usage{}, nil, nil)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if tc.wantPremature && !errors.Is(err, ErrPrematureStreamClose) {
				t.Errorf("error = %v, want ErrPrematureStreamClose", err)
			}
			if !tc.wantPremature && tc.wantErr && errors.Is(err, ErrPrematureStreamClose) {
				t.Errorf("error = %v, must not be ErrPrematureStreamClose", err)
			}
		})
	}
}

// abruptStreamProvider streams partial content and closes the channel
// without a finish_reason for the first maxFails calls, then heals and
// emits a proper stream.
type abruptStreamProvider struct {
	maxFails int
	fails    int
	calls    int
}

func (p *abruptStreamProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	return nil, errors.New("streaming only")
}

func (p *abruptStreamProvider) SendStream(ctx context.Context, req types.ChatRequest) (<-chan providers.StreamChunk, <-chan error) {
	chunks := make(chan providers.StreamChunk, 4)
	errs := make(chan error, 1)
	fail := p.fails < p.maxFails
	if fail {
		p.fails++
	}
	p.calls++
	go func() {
		defer close(chunks)
		defer close(errs)
		if fail {
			chunks <- providers.StreamChunk{Choices: []providers.StreamChoice{{
				Delta: providers.StreamDelta{Content: "partial text"},
			}}}
			return // connection dies before finish_reason
		}
		chunks <- providers.StreamChunk{Choices: []providers.StreamChoice{{
			Delta:        providers.StreamDelta{Content: "recovered"},
			FinishReason: strPtr("stop"),
		}}}
	}()
	return chunks, errs
}

func newStreamingClient(p Provider) *Client {
	return &Client{
		Provider:     p,
		Model:        "test-model",
		MaxRetries:   0,
		RetryBackoff: time.Millisecond,
		OnToken:      func(string) {},
	}
}

// TestCall_prematureCloseReplays pins the replay: an abruptly closed
// stream is replayed without consuming a retry slot, and the partial
// content never surfaces as a final answer.
func TestCall_prematureCloseReplays(t *testing.T) {
	p := &abruptStreamProvider{maxFails: 1}
	c := newStreamingClient(p)

	res, err := c.Call(context.Background(), types.ChatRequest{Model: "test-model"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Message.Content != "recovered" {
		t.Errorf("content = %q, want recovered", res.Message.Content)
	}
	if strings.Contains(res.Message.Content, "partial") {
		t.Error("partial content from the dead stream must be discarded")
	}
	if res.FinishReason != "stop" {
		t.Errorf("finish reason = %q, want stop", res.FinishReason)
	}
	if c.prematureCount != 1 {
		t.Errorf("prematureCount = %d, want 1", c.prematureCount)
	}
	if p.calls != 2 {
		t.Errorf("provider calls = %d, want 2", p.calls)
	}
}

// TestCall_prematureCloseSurfacesError pins the failure path: when the
// stream keeps dying and there is no fallback, Call returns the error
// instead of silently treating partial content as a final answer.
func TestCall_prematureCloseSurfacesError(t *testing.T) {
	p := &abruptStreamProvider{maxFails: 1000}
	c := newStreamingClient(p)

	res, err := c.Call(context.Background(), types.ChatRequest{Model: "test-model"})
	if err == nil {
		t.Fatal("expected error after repeated abrupt stream closes")
	}
	if !errors.Is(err, ErrPrematureStreamClose) {
		t.Errorf("error = %v, want ErrPrematureStreamClose", err)
	}
	if res.Message.Content != "" {
		t.Errorf("message content = %q, want empty (partial content discarded)", res.Message.Content)
	}
	// Two replays plus the original attempt.
	if p.calls != 3 {
		t.Errorf("provider calls = %d, want 3", p.calls)
	}
}

// TestCall_prematureCloseRotatesToFallback pins the ladder: after the
// replays are exhausted, Call swaps to the fallback provider.
func TestCall_prematureCloseRotatesToFallback(t *testing.T) {
	primary := &abruptStreamProvider{maxFails: 1000}
	fallback := &abruptStreamProvider{maxFails: 0}
	c := newStreamingClient(primary)
	c.FallbackProvider = fallback
	c.FallbackModel = "fallback-model"

	res, err := c.Call(context.Background(), types.ChatRequest{Model: "test-model"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Message.Content != "recovered" {
		t.Errorf("content = %q, want recovered from fallback", res.Message.Content)
	}
	if c.Provider != Provider(fallback) {
		t.Error("primary did not rotate to fallback")
	}
	if c.Model != "fallback-model" {
		t.Errorf("model = %q, want fallback-model", c.Model)
	}
}

// errorThenCloseStreamProvider streams partial content, pushes a real
// transport error onto errs, then closes both channels — the ordering
// real providers use. Once chunks is closed both select cases in
// runStream are ready and Go picks at random; the real error must win
// deterministically instead of being masked by ErrPrematureStreamClose.
type errorThenCloseStreamProvider struct {
	err error
}

func (p *errorThenCloseStreamProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	return nil, errors.New("streaming only")
}

func (p *errorThenCloseStreamProvider) SendStream(ctx context.Context, req types.ChatRequest) (<-chan providers.StreamChunk, <-chan error) {
	chunks := make(chan providers.StreamChunk, 4)
	errs := make(chan error, 1)
	go func() {
		defer close(chunks)
		defer close(errs)
		chunks <- providers.StreamChunk{Choices: []providers.StreamChoice{{
			Delta: providers.StreamDelta{Content: "partial text"},
		}}}
		errs <- p.err
	}()
	return chunks, errs
}

// closeErrsEarlyProvider closes errs BEFORE chunks, and only after streaming a
// finish chunk. Go's select picks uniformly among ready cases, so runStream can
// read the closed errs before the buffered chunk — and must not discard the
// buffered finish chunk as a premature close.
type closeErrsEarlyProvider struct{}

func (p *closeErrsEarlyProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	return nil, errors.New("streaming only")
}

func (p *closeErrsEarlyProvider) SendStream(ctx context.Context, req types.ChatRequest) (<-chan providers.StreamChunk, <-chan error) {
	chunks := make(chan providers.StreamChunk, 4)
	errs := make(chan error, 1)
	go func() {
		chunks <- providers.StreamChunk{Choices: []providers.StreamChoice{{
			Delta:        providers.StreamDelta{Content: "answer"},
			FinishReason: strPtr("stop"),
		}}}
		close(errs) // closed before chunks — the ordering the fix guards
		close(chunks)
	}()
	return chunks, errs
}

func TestRunStream_closedErrsDoesNotDiscardBufferedChunk(t *testing.T) {
	// Repeat so both select orders are exercised: a closed errs winning the race
	// must not discard the buffered finish chunk.
	for i := 0; i < 300; i++ {
		c := newStreamingClient(&closeErrsEarlyProvider{})
		msg, _, _, _, err := c.runStream(context.Background(), c.Provider.(StreamProvider), types.ChatRequest{Model: "test-model"})
		if err != nil {
			t.Fatalf("iteration %d: err = %v, want the buffered finish chunk assembled", i, err)
		}
		if msg.Content != "answer" {
			t.Fatalf("iteration %d: content = %q, want answer", i, msg.Content)
		}
	}
}

func TestRunStream_realErrorNotMaskedByPrematureClose(t *testing.T) {
	// Run several times: before the errs-drain fix, the select race
	// masked the real error roughly half the time.
	wantErr := errors.New("read stream: unexpected EOF")
	for i := 0; i < 20; i++ {
		c := newStreamingClient(&errorThenCloseStreamProvider{err: wantErr})
		_, _, _, _, err := c.runStream(context.Background(), c.Provider.(StreamProvider), types.ChatRequest{Model: "test-model"})
		if !errors.Is(err, wantErr) {
			t.Fatalf("iteration %d: error = %v, want %v (transport error must not be masked as premature close)", i, err, wantErr)
		}
	}
}

// splitUsageStreamProvider mimics the native Anthropic usage split: an
// early usage-only chunk carries the input tokens (message_start shape),
// a content chunk follows, and the finish chunk carries ONLY the output
// tokens (message_delta shape). Mirroring gen_ai.* attributes per chunk
// would let the output-only finish chunk overwrite
// gen_ai.usage.input_tokens with 0.
type splitUsageStreamProvider struct{}

func (p *splitUsageStreamProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	return nil, errors.New("streaming only")
}

func (p *splitUsageStreamProvider) SendStream(ctx context.Context, req types.ChatRequest) (<-chan providers.StreamChunk, <-chan error) {
	// Unbuffered on purpose: runStream's select also watches errs, and a
	// closed errs channel races buffered chunks into the nondeterministic
	// errs-nil early-close path. Rendezvous sends guarantee every chunk
	// is consumed before the producer can close anything.
	chunks := make(chan providers.StreamChunk)
	errs := make(chan error, 1)
	go func() {
		defer close(chunks)
		defer close(errs)
		chunks <- providers.StreamChunk{
			Model: "claude-test",
			Usage: &types.Usage{PromptTokens: 100},
		}
		chunks <- providers.StreamChunk{Choices: []providers.StreamChoice{{
			Delta: providers.StreamDelta{Content: "hello"},
		}}}
		chunks <- providers.StreamChunk{
			Choices: []providers.StreamChoice{{
				Delta:        providers.StreamDelta{},
				FinishReason: strPtr("stop"),
			}},
			Usage: &types.Usage{CompletionTokens: 42},
		}
	}()
	return chunks, errs
}

// TestRunStream_genAIAttrsMirrorAccumulatedUsage pins the fix for the
// Anthropic split-usage shape: the finished llm.stream span must carry
// the accumulated totals (input from the early chunk, output from the
// finish chunk), not the last chunk's partial usage.
func TestRunStream_genAIAttrsMirrorAccumulatedUsage(t *testing.T) {
	bp := observability.NewBufferingSpanProcessor()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(bp))
	old := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer func() {
		otel.SetTracerProvider(old)
		_ = tp.Shutdown(context.Background())
	}()

	c := newStreamingClient(&splitUsageStreamProvider{})
	c.OtelEnabled = true

	msg, finishReason, _, usage, err := c.runStream(
		context.Background(), c.Provider.(StreamProvider), types.ChatRequest{Model: "claude-test"})
	if err != nil {
		t.Fatalf("runStream: %v", err)
	}
	if msg.Content != "hello" {
		t.Errorf("content = %q, want hello", msg.Content)
	}
	if finishReason != "stop" {
		t.Errorf("finish reason = %q, want stop", finishReason)
	}
	if usage.PromptTokens != 100 || usage.CompletionTokens != 42 {
		t.Errorf("accumulated usage = %d in / %d out, want 100/42",
			usage.PromptTokens, usage.CompletionTokens)
	}

	traces := bp.Traces()
	if len(traces) != 1 {
		t.Fatalf("recorded %d spans, want 1 (llm.stream)", len(traces))
	}
	span := traces[0]
	if span.Name != "llm.stream" {
		t.Errorf("span name = %q, want llm.stream", span.Name)
	}
	if got := span.Attributes["gen_ai.usage.input_tokens"]; got != int64(100) {
		t.Errorf("gen_ai.usage.input_tokens = %v, want 100 (message_start input must survive the output-only finish chunk)", got)
	}
	if got := span.Attributes["gen_ai.usage.output_tokens"]; got != int64(42) {
		t.Errorf("gen_ai.usage.output_tokens = %v, want 42", got)
	}
}

// TestRunStream_errorKeepsPartialContent: a transport error after the model has
// streamed text must not erase that text. Before this, the deadline case
// returned an empty message, so a sub-agent that was almost done looked
// identical to one that produced nothing.
func TestRunStream_errorKeepsPartialContent(t *testing.T) {
	wantErr := errors.New("read stream: unexpected EOF")
	// Repeat: the provider buffers the content chunk and the error together, so
	// which select arm wins is uniform. The buffered chunk must be drained on
	// the error path either way (it was not, and Windows picked the error arm
	// first while Linux happened to read the chunk).
	for i := 0; i < 50; i++ {
		c := newStreamingClient(&errorThenCloseStreamProvider{err: wantErr})
		msg, _, _, _, err := c.runStream(context.Background(), c.Provider.(StreamProvider), types.ChatRequest{Model: "test-model"})
		if !errors.Is(err, wantErr) {
			t.Fatalf("iteration %d: error = %v, want %v", i, err, wantErr)
		}
		if msg.Content != "partial text" {
			t.Fatalf("iteration %d: content = %q, want the partial text", i, msg.Content)
		}
		if len(msg.ToolCalls) != 0 {
			t.Errorf("iteration %d: a failed stream must not expose partial tool calls, got %d", i, len(msg.ToolCalls))
		}
	}
}

// partialThenHangStreamProvider emits one content chunk and then never closes:
// the only way out is context cancellation, which is the timeout shape.
type partialThenHangStreamProvider struct{}

func (p *partialThenHangStreamProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	return nil, errors.New("streaming only")
}

func (p *partialThenHangStreamProvider) SendStream(ctx context.Context, req types.ChatRequest) (<-chan providers.StreamChunk, <-chan error) {
	chunks := make(chan providers.StreamChunk, 1)
	errs := make(chan error)
	chunks <- providers.StreamChunk{Choices: []providers.StreamChoice{{
		Delta: providers.StreamDelta{Content: "almost done"},
	}}}
	return chunks, errs
}

func TestRunStream_cancelKeepsPartialContent(t *testing.T) {
	c := newStreamingClient(&partialThenHangStreamProvider{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	msg, _, _, _, err := c.runStream(ctx, c.Provider.(StreamProvider), types.ChatRequest{Model: "test-model"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want DeadlineExceeded", err)
	}
	if msg.Content != "almost done" {
		t.Errorf("content = %q, want the partial text", msg.Content)
	}
}
