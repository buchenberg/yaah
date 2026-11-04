package llm

import (
	"context"
	"errors"
	"strings"
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
		"invalid_request_error: reasoning_content is not supported by this model [msgs=3 roles=user]")
}

// swapStateProvider models the fallback: after the swap it always fails
// with the thinking-replay 400 (a thinking-mode endpoint that cannot
// consume the swapped-out primary's history).
type swapStateProvider struct{ calls int }

func (p *swapStateProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	p.calls++
	return nil, statusError{
		msg:    "The `content[].thinking` in the thinking mode must be passed back to the API.",
		status: 400,
	}
}

// degeneratePrimaryProvider fails with a degenerate stream until it has
// been swapped away from (mirroring a provider whose stream cut before any
// content).
type degeneratePrimaryProvider struct{ calls int }

func (p *degeneratePrimaryProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	p.calls++
	return nil, errors.New("non-streaming response produced no content")
}

// TestCall_swapStatePersistsAcrossTurns pins the yaah-31j wiring: the
// swap happened in Call 1, but the mixed history keeps failing on the
// thinking-mode fallback in Call 2 and beyond — so the swap attribution
// must persist on the client, not in per-Call state.
func TestCall_swapStatePersistsAcrossTurns(t *testing.T) {
	primary := &degeneratePrimaryProvider{}
	fallback := &swapStateProvider{}
	c := &Client{
		Provider:         primary,
		FallbackProvider: fallback,
		Model:            "test-model",
		MaxRetries:       2,
		RetryBackoff:     time.Millisecond,
		StripReasoning:   func() {},
	}

	for turn := 1; turn <= 2; turn++ {
		_, err := c.Call(context.Background(), types.ChatRequest{Model: "test-model"})
		if err == nil {
			t.Fatalf("turn %d: expected the thinking-replay error to surface", turn)
		}
		if !strings.Contains(err.Error(), "swapped-out primary") {
			t.Fatalf("turn %d: error must attribute the failure to the mid-session swap, got: %s", turn, err.Error())
		}
		if strings.Contains(err.Error(), "set `thinking: true`") {
			t.Fatalf("turn %d: config advice must not be given after a swap, got: %s", turn, err.Error())
		}
	}
}

// TestCall_swapStatePersistsAcrossCredentialRotations exercises the
// credential-rotation swap path specifically: a typed 401 from the primary
// triggers ShouldRotateCred → swapToFallback, and the attribution must
// persist on the client exactly as it does for the degenerate-stream path
// (yaah-31j review: reverting the flag on this path must fail this test).
func TestCall_swapStatePersistsAcrossCredentialRotations(t *testing.T) {
	primary := &healingProvider{
		failErr:  statusError{msg: "request failed with status 401: invalid api key", status: 401},
		maxFails: 1000, // never heals
	}
	fallback := &swapStateProvider{}
	c := &Client{
		Provider:         primary,
		FallbackProvider: fallback,
		Model:            "primary-model",
		FallbackModel:    "fallback-model",
		MaxRetries:       2,
		RetryBackoff:     time.Millisecond,
	}

	for turn := 1; turn <= 2; turn++ {
		_, err := c.Call(context.Background(), types.ChatRequest{Model: "primary-model"})
		if err == nil {
			t.Fatalf("turn %d: expected the thinking-replay error to surface", turn)
		}
		if !strings.Contains(err.Error(), "swapped-out primary") {
			t.Fatalf("turn %d: error must attribute the failure to the credential-rotation swap, got: %s", turn, err.Error())
		}
	}
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
		failErr: errors.New("invalid_request_error: reasoning_content is not supported by this model [msgs=3 roles=user]"),
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

// statusError carries a typed HTTP status so Classify can read it, mirroring
// providers.APIError.
type statusError struct {
	msg    string
	status int
}

func (e statusError) Error() string        { return e.msg }
func (e statusError) HTTPStatus() int      { return e.status }
func (e statusError) ProviderCode() string { return "" }

// Mistral-hosted GLM rejects a replayed reasoning_content with 422
// extra_forbidden. That used to abort because the strip classification only
// ran for 400 and unknown status. The client must strip and replay on 422 too.
func TestCall_stripReasoningRecoversFrom422(t *testing.T) {
	p := &healingProvider{
		failErr: statusError{
			status: 422,
			msg:    `provider returned 422: {"detail":[{"type":"extra_forbidden","loc":["body","messages",2,"assistant","reasoning_content"],"msg":"Extra inputs are not permitted"}]}`,
		},
		response: &types.ChatResponse{
			Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "recovered"},
				FinishReason: "stop",
			}},
		},
		maxFails: 1,
	}
	strips := 0
	c := &Client{
		Provider:       p,
		Model:          "test-model",
		MaxRetries:     3,
		RetryBackoff:   time.Millisecond,
		StripReasoning: func() { strips++ },
	}

	res, err := c.Call(context.Background(), types.ChatRequest{Model: "test-model"})
	if err != nil {
		t.Fatalf("Call() error after 422 strip recovery: %v", err)
	}
	if res.Message.Content != "recovered" {
		t.Fatalf("content = %q, want %q", res.Message.Content, "recovered")
	}
	if strips == 0 {
		t.Error("expected a strip attempt before recovering from the 422")
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

// A provider in thinking mode demands reasoning_content be replayed. Stripping
// is the OPPOSITE of the remedy: it guarantees the identical failure forever.
// The client must classify it as config-actionable and abort, not strip-retry.
func TestCall_thinkingRequiredAbortsWithoutStripping(t *testing.T) {
	p := &healingProvider{
		failErr:  errors.New("invalid_request_error: The `content[].thinking` in the thinking mode must be passed back to the API."),
		maxFails: 100,
	}
	strips := 0
	c := &Client{
		Provider:       p,
		Model:          "test-model",
		MaxRetries:     3,
		RetryBackoff:   time.Millisecond,
		StripReasoning: func() { strips++ },
	}

	_, err := c.Call(context.Background(), types.ChatRequest{Model: "test-model"})
	if err == nil {
		t.Fatal("expected a non-retryable error")
	}
	if strips != 0 {
		t.Errorf("StripReasoning invoked %d times — stripping cannot satisfy a provider demanding the content back", strips)
	}
	if p.stripped != 1 {
		t.Errorf("provider called %d times, want 1 (non-retryable abort)", p.stripped)
	}
}
