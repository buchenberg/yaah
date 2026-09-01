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
			_, _, _, _, err := checkTruncatedStream(tc.content, nil, tc.finishReason, "model-x", "", types.Usage{}, nil)
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
