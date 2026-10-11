package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/types"
)

var okResponse = &types.ChatResponse{
	Choices: []types.Choice{{
		Message:      types.Message{Role: "assistant", Content: "ok"},
		FinishReason: "stop",
	}},
}

// TestCall_fallbackSwapsOnCredentialError pins the rotation: a 401 from
// the primary swaps to the fallback provider (and the models swap with
// it), then the call succeeds (plan 8.x).
func TestCall_fallbackSwapsOnCredentialError(t *testing.T) {
	primary := &healingProvider{
		failErr:  errors.New("request failed with status 401: invalid api key"),
		response: &types.ChatResponse{Choices: []types.Choice{{Message: types.Message{Content: "from-primary"}, FinishReason: "stop"}}},
		maxFails: 1000, // never heals
	}
	fallback := &healingProvider{
		response: &types.ChatResponse{Choices: []types.Choice{{Message: types.Message{Content: "from-fallback"}, FinishReason: "stop"}}},
	}
	c := &Client{
		Provider:         primary,
		FallbackProvider: fallback,
		Model:            "primary-model",
		FallbackModel:    "fallback-model",
		MaxRetries:       2,
		RetryBackoff:     time.Millisecond,
	}

	res, err := c.Call(context.Background(), types.ChatRequest{Model: "primary-model"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Message.Content != "from-fallback" {
		t.Errorf("content = %q, want from-fallback", res.Message.Content)
	}
	// Provider and model must swap permanently for subsequent calls.
	if c.Provider != Provider(fallback) {
		t.Error("primary did not rotate to fallback")
	}
	if c.FallbackProvider != Provider(primary) {
		t.Error("old primary not preserved as fallback")
	}
	if c.Model != "fallback-model" {
		t.Errorf("model = %q, want fallback-model", c.Model)
	}
}

// TestCall_compactionAdoptedOnSuccess pins the Compacted return path
// (B6): when Compact fires mid-Call and the retry heals, the success
// result carries the compacted slice for the loop to adopt.
func TestCall_compactionAdoptedOnSuccess(t *testing.T) {
	p := &healingProvider{
		failErr:  errors.New("context length exceeded: maximum tokens 4096"),
		response: okResponse,
		maxFails: 1,
	}
	long := types.Message{Role: "user", Content: strings.Repeat("x", 400)}
	short := []types.Message{{Role: "system", Content: "compacted"}}
	c := &Client{
		Provider:      p,
		Model:         "test-model",
		MaxRetries:    0,
		ContextWindow: 1000,
		Compact: func(ctx context.Context, messages []types.Message, threshold float64) []types.Message {
			return short
		},
	}

	res, err := c.Call(context.Background(), types.ChatRequest{
		Model:    "test-model",
		Messages: []types.Message{{Role: "system", Content: "sys"}, long},
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !res.Compacted {
		t.Fatal("result.Compacted not set after overflow compaction")
	}
	if len(res.CompactedMessages) != len(short) || res.CompactedMessages[0].Content != "compacted" {
		t.Errorf("CompactedMessages = %+v, want the compacted slice", res.CompactedMessages)
	}
}

// TestCall_compactionAdoptedOnError pins the error-path attachment: a
// compaction that fires but cannot rescue the call still reports the
// compacted slice so the loop's in-memory state matches the rebased
// persistence baseline.
func TestCall_compactionAdoptedOnError(t *testing.T) {
	p := &healingProvider{
		failErr:  errors.New("context length exceeded: maximum tokens 4096"),
		response: okResponse,
		maxFails: 1000, // never heals
	}
	short := []types.Message{{Role: "system", Content: "compacted"}}
	c := &Client{
		Provider:      p,
		Model:         "test-model",
		MaxRetries:    0,
		ContextWindow: 1000,
		Compact: func(ctx context.Context, messages []types.Message, threshold float64) []types.Message {
			return short
		},
	}

	res, err := c.Call(context.Background(), types.ChatRequest{
		Model:    "test-model",
		Messages: []types.Message{{Role: "user", Content: strings.Repeat("x", 400)}},
	})
	if err == nil {
		t.Fatal("expected the unresolvable overflow error")
	}
	if !res.Compacted {
		t.Fatal("error result must carry Compacted=true (B6)")
	}
	if len(res.CompactedMessages) != 1 || res.CompactedMessages[0].Content != "compacted" {
		t.Errorf("CompactedMessages = %+v, want the compacted slice", res.CompactedMessages)
	}
}

// TestCall_degenerateStreamReplays pins the empty-response replay: one
// degenerate response is replayed without consuming a retry slot.
func TestCall_degenerateStreamReplays(t *testing.T) {
	p := &healingProvider{
		failErr:  errors.New("non-streaming response produced no content (finish_reason=stop)"),
		response: okResponse,
		maxFails: 1,
	}
	c := &Client{
		Provider:   p,
		Model:      "test-model",
		MaxRetries: 0, // replay must not need retries
	}

	res, err := c.Call(context.Background(), types.ChatRequest{Model: "test-model"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Message.Content != "ok" {
		t.Errorf("content = %q, want ok", res.Message.Content)
	}
	if c.replayCount != 1 {
		t.Errorf("replayCount = %d, want 1", c.replayCount)
	}
}

// respFormatProvider rejects any request that carries a response_format
// with a 400, emulating LM Studio (which accepts only json_schema/text,
// not json_object). It records the response_format seen on each call so
// tests can assert the drop-and-replay.
type respFormatProvider struct {
	sawFormat []*types.ResponseFormat
}

func (p *respFormatProvider) Send(_ context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	p.sawFormat = append(p.sawFormat, req.ResponseFormat)
	if req.ResponseFormat != nil {
		return nil, &providers.APIError{
			StatusCode: 400,
			Message:    `'response_format.type' must be 'json_schema' or 'text'`,
		}
	}
	return okResponse, nil
}

// TestCall_dropsResponseFormatOn400 pins the graceful degradation for
// yaah-n7k: when a provider rejects response_format, the client drops it
// and replays once instead of aborting the call. MaxRetries is 0 so the
// test proves the drop-replay consumes no retry slot — with a nonzero
// budget, removing the attempt decrement would still pass.
func TestCall_dropsResponseFormatOn400(t *testing.T) {
	p := &respFormatProvider{}
	c := &Client{Provider: p, Model: "m", MaxRetries: 0, RetryBackoff: time.Millisecond}

	res, err := c.Call(context.Background(), types.ChatRequest{
		Model:          "m",
		Messages:       []types.Message{{Role: "user", Content: "hi"}},
		ResponseFormat: &types.ResponseFormat{Type: "json_object"},
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Message.Content != "ok" {
		t.Errorf("content = %q, want ok", res.Message.Content)
	}
	if len(p.sawFormat) != 2 {
		t.Fatalf("provider calls = %d, want exactly 2 (reject, then recover without the format)", len(p.sawFormat))
	}
	if p.sawFormat[0] == nil {
		t.Error("first attempt should carry response_format")
	}
	if last := p.sawFormat[len(p.sawFormat)-1]; last != nil {
		t.Errorf("recovering attempt must drop response_format, got %+v", last)
	}
}

// alwaysFail400Provider rejects every request with a response_format 400,
// even after the format has been dropped.
type alwaysFail400Provider struct{ calls int }

func (p *alwaysFail400Provider) Send(_ context.Context, _ types.ChatRequest) (*types.ChatResponse, error) {
	p.calls++
	return nil, &providers.APIError{StatusCode: 400, Message: "response_format unsupported"}
}

// TestCall_dropResponseFormatBounded ensures the drop is a one-shot
// recovery: a provider that keeps rejecting must not loop forever.
func TestCall_dropResponseFormatBounded(t *testing.T) {
	p := &alwaysFail400Provider{}
	c := &Client{Provider: p, Model: "m", MaxRetries: 5, RetryBackoff: time.Millisecond}

	_, err := c.Call(context.Background(), types.ChatRequest{
		Model:          "m",
		Messages:       []types.Message{{Role: "user", Content: "hi"}},
		ResponseFormat: &types.ResponseFormat{Type: "json_object"},
	})
	if err == nil {
		t.Fatal("expected error when provider keeps rejecting response_format")
	}
	if p.calls > 3 {
		t.Errorf("drop-and-retry must be bounded; made %d calls", p.calls)
	}
}
