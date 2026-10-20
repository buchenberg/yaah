package llm

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/observability"
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
	ceiling   int
}

func (p *outputBudgetProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	p.calls++
	p.caps = append(p.caps, req.MaxTokens)
	if p.calls <= p.failCalls {
		return nil, errors.New("non-streaming response produced no content (finish_reason=length): output token budget exhausted before any content")
	}
	return okResponse, nil
}

func (p *outputBudgetProvider) SendStream(context.Context, types.ChatRequest) (<-chan providers.StreamChunk, <-chan error) {
	return nil, nil
}

func (p *outputBudgetProvider) EffectiveMaxTokens(req types.ChatRequest) int {
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	return p.effective
}

func (p *outputBudgetProvider) OutputTokenCeiling(types.ChatRequest) int { return p.ceiling }

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
// max_tokens unset) must not be retried: an explicit floor could ask for
// fewer tokens than the server's unknown default already used.
func TestRaisedOutputBudgetSkipsUnknownCap(t *testing.T) {
	p := &providers.OpenAIClient{} // DefaultMaxTokens zero
	if next, ok := raisedOutputBudget(p, types.ChatRequest{}); ok {
		t.Fatalf("raised cap = %d, ok = true; want no raise when the cap is unknown", next)
	}
}

// OTel instrumentation must forward the optional output-cap capability.
func TestRaisedOutputBudgetThroughInstrumentedProvider(t *testing.T) {
	p := &outputBudgetProvider{effective: 16384}
	wrapped := &observability.InstrumentedProvider{Inner: p}
	next, ok := raisedOutputBudget(wrapped, types.ChatRequest{})
	if !ok || next != 32768 {
		t.Fatalf("raised cap = %d, ok = %v; want 32768, true through the OTel wrapper", next, ok)
	}
}

// The native Anthropic safe floor must not be doubled; compatible endpoints
// can be raised, clamped to a known model output limit.
func TestRaisedOutputBudgetHonorsProviderCeiling(t *testing.T) {
	native := providers.NewAnthropicClient("https://api.anthropic.com", "sk-test", 0)
	if next, ok := raisedOutputBudget(native, types.ChatRequest{Model: "claude-3-haiku"}); ok {
		t.Fatalf("native Anthropic cap raised to %d; want no raise past the 4096 safe floor", next)
	}

	compatible := providers.NewAnthropicClient("https://api.z.ai/api/anthropic", "sk-test", 0)
	next, ok := raisedOutputBudget(compatible, types.ChatRequest{Model: "GLM-5.3"})
	if !ok || next != 32768 {
		t.Fatalf("compatible cap = %d, ok = %v; want 32768, true", next, ok)
	}
}

// A known model output limit clamps the doubled cap instead of crossing it.
func TestRaisedOutputBudgetClampsToModelCeiling(t *testing.T) {
	openai := providers.NewOpenAIClient("https://api.openai.com", "sk-test", 0)
	next, ok := raisedOutputBudget(openai, types.ChatRequest{Model: "gpt-4o", MaxTokens: 10000})
	if !ok || next != 16384 {
		t.Fatalf("gpt-4o raised cap = %d, ok = %v; want 16384 (model ceiling), true", next, ok)
	}

	compatible := providers.NewAnthropicClient("https://api.z.ai/api/anthropic", "sk-test", 0)
	next, ok = raisedOutputBudget(compatible, types.ChatRequest{Model: "GLM-5.3", MaxTokens: 16384})
	if !ok || next != 32768 {
		t.Fatalf("GLM-5.3 raised cap = %d, ok = %v; want 32768, true", next, ok)
	}
}

// recoveryRejectProvider exhausts the first budget, then rejects the raised
// cap with a 400, as providers do when max_tokens exceeds the model limit.
type recoveryRejectProvider struct {
	calls int
}

func (p *recoveryRejectProvider) Send(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
	p.calls++
	if p.calls == 1 {
		return &types.ChatResponse{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant"},
				FinishReason: "length",
			}},
			Usage: types.Usage{PromptTokens: 100, CompletionTokens: 10000, TotalTokens: 10100},
		}, nil
	}
	return nil, &providers.APIError{
		StatusCode: 400,
		Message:    "invalid request: max completion tokens exceeds the model limit",
	}
}

func (p *recoveryRejectProvider) EffectiveMaxTokens(req types.ChatRequest) int { return req.MaxTokens }
func (p *recoveryRejectProvider) OutputTokenCeiling(types.ChatRequest) int     { return 16384 }

// A failed raised-cap retry must not replace the actionable budget diagnosis
// with a generic format-error abort.
func TestCall_outputBudgetRejectionKeepsDiagnosis(t *testing.T) {
	p := &recoveryRejectProvider{}
	c := &Client{
		Provider:     p,
		Model:        "test-model",
		MaxRetries:   2,
		RetryBackoff: time.Millisecond,
	}

	res, err := c.Call(context.Background(), types.ChatRequest{
		Model:     "test-model",
		MaxTokens: 10000,
	})
	if err == nil {
		t.Fatal("expected the raised-cap retry to fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "output token budget exhausted") {
		t.Errorf("error = %v, want the original output-budget diagnosis preserved", err)
	}
	if !strings.Contains(msg, "provider returned 400") {
		t.Errorf("error = %v, want the provider's rejection included", err)
	}
	if p.calls != 2 {
		t.Fatalf("provider called %d times, want 2 (initial + one raised retry)", p.calls)
	}
	if res.RecoveryUsage.TotalTokens != 10100 {
		t.Errorf("RecoveryUsage.TotalTokens = %d, want 10100", res.RecoveryUsage.TotalTokens)
	}
}

// exhaustedUsageProvider returns an empty finish_reason=length response with
// billable usage, then a normal response.
type exhaustedUsageProvider struct {
	calls     int
	failUsage types.Usage
	okUsage   types.Usage
}

func (p *exhaustedUsageProvider) Send(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
	p.calls++
	if p.calls == 1 {
		return &types.ChatResponse{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant"},
				FinishReason: "length",
			}},
			Usage: p.failUsage,
		}, nil
	}
	return &types.ChatResponse{
		Choices: []types.Choice{{
			Message:      types.Message{Role: "assistant", Content: "ok"},
			FinishReason: "stop",
		}},
		Usage: p.okUsage,
	}, nil
}

func (p *exhaustedUsageProvider) EffectiveMaxTokens(req types.ChatRequest) int {
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	return 16384
}

func (p *exhaustedUsageProvider) OutputTokenCeiling(types.ChatRequest) int { return 0 }

// Usage billed by the exhausted attempt must survive the replay separately
// from the final attempt's Usage.
func TestCall_outputBudgetPreservesExhaustedUsage(t *testing.T) {
	failUsage := types.Usage{
		PromptTokens:       120,
		CompletionTokens:   16384,
		TotalTokens:        16504,
		CachedPromptTokens: 20,
		CompletionTokensDetails: &types.CompletionTokensDetails{
			ReasoningTokens: 16000,
		},
	}
	okUsage := types.Usage{PromptTokens: 130, CompletionTokens: 12, TotalTokens: 142}
	p := &exhaustedUsageProvider{failUsage: failUsage, okUsage: okUsage}
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
	if !reflect.DeepEqual(res.Usage, okUsage) {
		t.Errorf("Usage = %+v, want final attempt usage %+v", res.Usage, okUsage)
	}
	if !reflect.DeepEqual(res.RecoveryUsage, failUsage) {
		t.Errorf("RecoveryUsage = %+v, want exhausted attempt usage %+v", res.RecoveryUsage, failUsage)
	}
}
