package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
