package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/types"
)

// stripReasoningProvider always fails with the error message that
// classifies as ShouldStripReasoning, simulating a provider that
// persistently rejects reasoning content no matter how often it is
// stripped from history.
type stripReasoningProvider struct {
	calls int
}

func (p *stripReasoningProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	p.calls++
	return nil, errors.New(
		"invalid_request_error: reasoning_content in the thinking mode must be passed back [msgs=3 roles=user]")
}

func TestCall_stripReasoningRetryIsBounded(t *testing.T) {
	p := &stripReasoningProvider{}
	c := &Client{
		Provider:       p,
		Model:          "test-model",
		MaxRetries:     2,
		RetryBackoff:   time.Millisecond,
		StripReasoning: func() {},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.Call(ctx, types.ChatRequest{Model: "test-model"})
	if err == nil {
		t.Fatal("expected error after bounded retries")
	}

	// Strip attempts (capped at 3) do not advance the attempt counter;
	// everything else is bounded by MaxRetries. Total provider calls must
	// stay within strip cap + retries + margin — never unbounded.
	maxCalls := 3 + 2 + 2
	if p.calls > maxCalls {
		t.Fatalf("provider called %d times — strip-reasoning retry is unbounded (A2), want <= %d", p.calls, maxCalls)
	}
}

func TestCall_stripReasoningRecoversWhenProviderHeals(t *testing.T) {
	p := &healingProvider{
		failErr: errors.New("invalid_request_error: reasoning_content in the thinking mode must be passed back [msgs=3 roles=user]"),
		response: &types.ChatResponse{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "recovered"},
				FinishReason: "stop",
			}},
		},
		maxFails: 1,
	}
	c := &Client{
		Provider:       p,
		Model:          "test-model",
		MaxRetries:     3,
		RetryBackoff:   time.Millisecond,
		StripReasoning: func() {},
	}

	res, err := c.Call(context.Background(), types.ChatRequest{Model: "test-model"})
	if err != nil {
		t.Fatalf("Call() error after strip recovery: %v", err)
	}
	if res.Message.Content != "recovered" {
		t.Fatalf("content = %q, want %q", res.Message.Content, "recovered")
	}
	if p.stripped == 0 {
		t.Error("expected at least one strip attempt before recovery")
	}
}

// healingProvider fails with failErr while stripped < maxFails, then
// succeeds with the canned response. stripped counts the failures.
type healingProvider struct {
	failErr  error
	response *types.ChatResponse
	maxFails int
	stripped int
}

func (p *healingProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	if p.stripped < p.maxFails {
		p.stripped++
		return nil, p.failErr
	}
	return p.response, nil
}
