package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// startHeartbeatCounter drains hb in the background and returns a stop
// function that halts the collector and reports how many heartbeats were
// observed.
func startHeartbeatCounter(hb <-chan struct{}) func() int {
	count := 0
	stopCh := make(chan struct{})
	collectDone := make(chan struct{})
	go func() {
		defer close(collectDone)
		for {
			select {
			case <-hb:
				count++
			case <-stopCh:
				return
			}
		}
	}()
	return func() int {
		close(stopCh)
		<-collectDone
		return count
	}
}

// reasoningStreamProvider streams reasoning-only deltas for a while before
// finishing with a content chunk — the shape of a thinking model's long
// silent (content-free) phase that starved the stuck-child watchdog
// (yaah-1ep).
type reasoningStreamProvider struct {
	reasoningChunks int
	delay           time.Duration
}

func (p *reasoningStreamProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	return nil, errors.New("streaming only")
}

func (p *reasoningStreamProvider) SendStream(ctx context.Context, req types.ChatRequest) (<-chan providers.StreamChunk, <-chan error) {
	chunks := make(chan providers.StreamChunk)
	errs := make(chan error, 1)
	go func() {
		defer close(chunks)
		defer close(errs)
		for i := 0; i < p.reasoningChunks; i++ {
			select {
			case chunks <- providers.StreamChunk{Choices: []providers.StreamChoice{{
				Delta: providers.StreamDelta{ReasoningContent: "thinking..."},
			}}}:
			case <-ctx.Done():
				return
			}
			time.Sleep(p.delay)
		}
		select {
		case chunks <- providers.StreamChunk{Choices: []providers.StreamChoice{{
			Delta:        providers.StreamDelta{Content: "answer"},
			FinishReason: strPtr("stop"),
		}}}:
		case <-ctx.Done():
		}
	}()
	return chunks, errs
}

func TestCall_reasoningStreamEmitsHeartbeats(t *testing.T) {
	hb := make(chan struct{}, 1)
	ctx := tools.WithSubAgentHeartbeat(context.Background(), hb)
	getCount := startHeartbeatCounter(hb)

	p := &reasoningStreamProvider{reasoningChunks: 60, delay: 5 * time.Millisecond}
	c := &Client{
		Provider:          p,
		Model:             "test-model",
		OnToken:           func(string) {},
		HeartbeatInterval: 20 * time.Millisecond,
	}

	res, err := c.Call(ctx, types.ChatRequest{Model: "test-model"})
	count := getCount()

	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if res.Message.Content != "answer" {
		t.Errorf("content = %q, want %q", res.Message.Content, "answer")
	}
	// ~300ms of reasoning-only streaming at a 20ms throttle yields ~15
	// heartbeats. Before the fix, reasoning chunks emitted none and only
	// the attempt-start ping plus the final content chunk counted (2).
	if count < 5 {
		t.Errorf("heartbeats during reasoning-only stream = %d, want >= 5 (reasoning chunks must count as liveness)", count)
	}
}

// toolCallStreamProvider streams only tool-call deltas (no content, no
// reasoning) — the other silent phase that starved the watchdog.
type toolCallStreamProvider struct {
	chunks int
	delay  time.Duration
}

func (p *toolCallStreamProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	return nil, errors.New("streaming only")
}

func (p *toolCallStreamProvider) SendStream(ctx context.Context, req types.ChatRequest) (<-chan providers.StreamChunk, <-chan error) {
	chunks := make(chan providers.StreamChunk)
	errs := make(chan error, 1)
	go func() {
		defer close(chunks)
		defer close(errs)
		for i := 0; i < p.chunks; i++ {
			// Fragments must assemble into valid JSON: checkTruncatedStream
			// drops tool calls whose arguments fail json.Valid.
			frag := "x"
			if i == 0 {
				frag = `{"pad":"`
			}
			if i == p.chunks-1 {
				frag = `"}`
			}
			chunk := providers.StreamChunk{Choices: []providers.StreamChoice{{
				Delta: providers.StreamDelta{ToolCalls: []types.ToolCall{{
					Index: 0,
					Function: types.ToolCallFn{
						Arguments: frag,
					},
				}}},
			}}}
			if i == 0 {
				chunk.Choices[0].Delta.ToolCalls[0].ID = "call-1"
				chunk.Choices[0].Delta.ToolCalls[0].Type = "function"
				chunk.Choices[0].Delta.ToolCalls[0].Function.Name = "read"
			}
			select {
			case chunks <- chunk:
			case <-ctx.Done():
				return
			}
			time.Sleep(p.delay)
		}
		select {
		case chunks <- providers.StreamChunk{Choices: []providers.StreamChoice{{
			FinishReason: strPtr("tool_calls"),
		}}}:
		case <-ctx.Done():
		}
	}()
	return chunks, errs
}

func TestCall_toolCallStreamEmitsHeartbeats(t *testing.T) {
	hb := make(chan struct{}, 1)
	ctx := tools.WithSubAgentHeartbeat(context.Background(), hb)
	getCount := startHeartbeatCounter(hb)

	p := &toolCallStreamProvider{chunks: 60, delay: 5 * time.Millisecond}
	c := &Client{
		Provider:          p,
		Model:             "test-model",
		OnToken:           func(string) {},
		HeartbeatInterval: 20 * time.Millisecond,
	}

	_, err := c.Call(ctx, types.ChatRequest{Model: "test-model"})
	count := getCount()

	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if count < 5 {
		t.Errorf("heartbeats during tool-call-only stream = %d, want >= 5 (tool-call chunks must count as liveness)", count)
	}
}

// delayedFirstChunkProvider stalls before emitting the first stream chunk —
// the time-to-first-token window that a single attempt-start ping cannot
// cover (review follow-up on yaah-1ep).
type delayedFirstChunkProvider struct {
	delay time.Duration
}

func (p *delayedFirstChunkProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	return nil, errors.New("streaming only")
}

func (p *delayedFirstChunkProvider) SendStream(ctx context.Context, req types.ChatRequest) (<-chan providers.StreamChunk, <-chan error) {
	chunks := make(chan providers.StreamChunk)
	errs := make(chan error, 1)
	go func() {
		defer close(chunks)
		defer close(errs)
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return
		}
		select {
		case chunks <- providers.StreamChunk{Choices: []providers.StreamChoice{{
			Delta:        providers.StreamDelta{Content: "late answer"},
			FinishReason: strPtr("stop"),
		}}}:
		case <-ctx.Done():
		}
	}()
	return chunks, errs
}

func TestCall_TTFTWaitEmitsHeartbeats(t *testing.T) {
	hb := make(chan struct{}, 1)
	ctx := tools.WithSubAgentHeartbeat(context.Background(), hb)
	getCount := startHeartbeatCounter(hb)

	p := &delayedFirstChunkProvider{delay: 200 * time.Millisecond}
	c := &Client{
		Provider:          p,
		Model:             "test-model",
		OnToken:           func(string) {},
		HeartbeatInterval: 20 * time.Millisecond,
	}

	res, err := c.Call(ctx, types.ChatRequest{Model: "test-model"})
	count := getCount()

	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if res.Message.Content != "late answer" {
		t.Errorf("content = %q, want %q", res.Message.Content, "late answer")
	}
	// A 200ms first-token wait at a 20ms throttle yields ~10 heartbeats.
	// Before the wait ticker, only the attempt-start ping and the single
	// content chunk beat (2) — a wait longer than StuckChildTimeout was
	// still a false-positive kill.
	if count < 5 {
		t.Errorf("heartbeats during first-token wait = %d, want >= 5 (TTFT wait must emit liveness)", count)
	}
}

// slowSendProvider blocks in the non-streaming Send for a while before
// responding — a slow blocking call with no stream to beat from.
type slowSendProvider struct {
	delay time.Duration
}

func (p *slowSendProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	select {
	case <-time.After(p.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return okResponse, nil
}

func TestCall_nonStreamingSendEmitsHeartbeats(t *testing.T) {
	hb := make(chan struct{}, 1)
	ctx := tools.WithSubAgentHeartbeat(context.Background(), hb)
	getCount := startHeartbeatCounter(hb)

	c := &Client{
		Provider:          &slowSendProvider{delay: 150 * time.Millisecond},
		Model:             "test-model",
		HeartbeatInterval: 20 * time.Millisecond,
	}

	if _, err := c.Call(ctx, types.ChatRequest{Model: "test-model"}); err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	count := getCount()
	// ~7 beats during the blocking send; before the wait ticker only the
	// attempt-start ping (1).
	if count < 3 {
		t.Errorf("heartbeats during blocking non-streaming send = %d, want >= 3", count)
	}
}

func TestCall_retryBackoffEmitsHeartbeats(t *testing.T) {
	hb := make(chan struct{}, 1)
	ctx := tools.WithSubAgentHeartbeat(context.Background(), hb)
	getCount := startHeartbeatCounter(hb)

	// Unknown errors classify as retryable, so the first two attempts fail
	// and the third succeeds — exercising two backoff waits (40ms + 80ms).
	p := &healingProvider{
		failErr:  errors.New("boom"),
		maxFails: 2,
		response: okResponse,
	}
	c := &Client{
		Provider:          p,
		Model:             "test-model",
		MaxRetries:        2,
		RetryBackoff:      40 * time.Millisecond,
		HeartbeatInterval: 10 * time.Millisecond,
	}

	if _, err := c.Call(ctx, types.ChatRequest{Model: "test-model"}); err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	count := getCount()
	// ~12 beats across both backoff windows plus 3 attempt-start pings;
	// before the wait ticker, only the 3 pings.
	if count < 6 {
		t.Errorf("heartbeats across retry backoff = %d, want >= 6 (backoff waits must emit liveness)", count)
	}
}
