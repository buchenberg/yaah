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

// outputBudgetProvider fails with an output-budget error for the first
// failCalls requests, recording the max_tokens each request carried.
type outputBudgetProvider struct {
	calls     int
	caps      []int
	failCalls int
	effective int
}

func (p *outputBudgetProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	p.calls++
	p.caps = append(p.caps, req.MaxTokens)
	if p.calls <= p.failCalls {
		return nil, errors.New("non-streaming response produced no content (finish_reason=length): output token budget exhausted before any content")
	}
	return okResponse, nil
}

func (p *outputBudgetProvider) EffectiveMaxTokens(req types.ChatRequest) int {
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	return p.effective
}

// The first budget exhaustion must retry once with a doubled cap, without
// consuming a MaxRetries slot.
func TestCall_outputBudgetDoublesExplicitCap(t *testing.T) {
	p := &outputBudgetProvider{failCalls: 1}
	c := &Client{
		Provider:     p,
		Model:        "test-model",
		MaxRetries:   0,
		RetryBackoff: time.Millisecond,
	}

	res, err := c.Call(context.Background(), types.ChatRequest{
		Model:     "test-model",
		MaxTokens: 4096,
	})
	if err != nil {
		t.Fatalf("Call() error after raised-budget retry: %v", err)
	}
	if res.Message.Content != "ok" {
		t.Errorf("content = %q, want ok", res.Message.Content)
	}
	if p.calls != 2 {
		t.Fatalf("provider called %d times, want 2", p.calls)
	}
	if got, want := p.caps, []int{4096, 8192}; got[0] != want[0] || got[1] != want[1] {
		t.Errorf("max_tokens sequence = %v, want %v", got, want)
	}
}

// When the caller leaves max_tokens unset, the provider's effective default
// is the value that must be doubled.
func TestCall_outputBudgetDoublesProviderDefault(t *testing.T) {
	p := &outputBudgetProvider{failCalls: 1, effective: 16384}
	c := &Client{
		Provider:     p,
		Model:        "test-model",
		MaxRetries:   0,
		RetryBackoff: time.Millisecond,
	}

	if _, err := c.Call(context.Background(), types.ChatRequest{Model: "test-model"}); err != nil {
		t.Fatalf("Call() error after raised-budget retry: %v", err)
	}
	if p.calls != 2 {
		t.Fatalf("provider called %d times, want 2", p.calls)
	}
	if p.caps[0] != 0 {
		t.Errorf("first request max_tokens = %d, want unset (0)", p.caps[0])
	}
	if p.caps[1] != 32768 {
		t.Errorf("retried max_tokens = %d, want 32768 (2x provider default)", p.caps[1])
	}
}

// Raising the cap is a single recovery: a provider that exhausts the doubled
// budget too must surface the classified diagnosis rather than retry forever.
func TestCall_outputBudgetRetryIsBounded(t *testing.T) {
	p := &outputBudgetProvider{failCalls: 100}
	c := &Client{
		Provider:     p,
		Model:        "test-model",
		MaxRetries:   2,
		RetryBackoff: time.Millisecond,
	}

	_, err := c.Call(context.Background(), types.ChatRequest{
		Model:     "test-model",
		MaxTokens: 4096,
	})
	if err == nil {
		t.Fatal("expected an error when the doubled cap is exhausted too")
	}
	if !strings.Contains(err.Error(), "output token budget exhausted") {
		t.Errorf("error = %v, want the actionable classification message", err)
	}
	if p.calls != 2 {
		t.Fatalf("provider called %d times, want 2 (initial + one raised retry)", p.calls)
	}
	if got, want := p.caps, []int{4096, 8192}; got[0] != want[0] || got[1] != want[1] {
		t.Errorf("max_tokens sequence = %v, want %v", got, want)
	}
}

// raisedOutputBudget must stop at the ceiling instead of requesting an
// unbounded output budget.
func TestRaisedOutputBudgetCeiling(t *testing.T) {
	req := types.ChatRequest{MaxTokens: outputBudgetRetryCeiling}
	if _, ok := raisedOutputBudget(nil, req); ok {
		t.Fatal("raising at the ceiling must report ok=false")
	}

	req.MaxTokens = outputBudgetRetryCeiling / 2
	next, ok := raisedOutputBudget(nil, req)
	if !ok || next != outputBudgetRetryCeiling {
		t.Fatalf("raised cap = %d, ok = %v; want %d, true", next, ok, outputBudgetRetryCeiling)
	}
}

// A provider that reports no effective cap (OpenAI-compatible with
// max_tokens unset) still gets a usable retry floor.
func TestRaisedOutputBudgetUsesFloorWhenUnknown(t *testing.T) {
	p := &providers.OpenAIClient{} // DefaultMaxTokens zero
	next, ok := raisedOutputBudget(p, types.ChatRequest{})
	if !ok || next != outputBudgetRetryFloor*2 {
		t.Fatalf("raised cap = %d, ok = %v; want %d, true", next, ok, outputBudgetRetryFloor*2)
	}
}
