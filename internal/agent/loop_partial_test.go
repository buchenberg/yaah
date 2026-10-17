package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// partialThenHangProvider streams text and then stalls until the context ends —
// the shape of a completion that is almost done when its deadline fires.
type partialThenHangProvider struct{}

func (p *partialThenHangProvider) Send(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
	return nil, errors.New("streaming only")
}

func (p *partialThenHangProvider) SendStream(ctx context.Context, req types.ChatRequest) (<-chan providers.StreamChunk, <-chan error) {
	chunks := make(chan providers.StreamChunk, 1)
	errs := make(chan error) // never closed: only the context can end this stream
	chunks <- providers.StreamChunk{Choices: []providers.StreamChoice{{
		Delta: providers.StreamDelta{Content: "almost done"},
	}}}
	return chunks, errs
}

// TestLoop_RunKeepsPartialContentOnDeadline: the loop used to return "" with
// the deadline error, which is why a timed-out sub-agent reported no output at
// all. The partial text must travel with the error so the dispatcher can put it
// in the result; it must not be appended to the conversation.
func TestLoop_RunKeepsPartialContentOnDeadline(t *testing.T) {
	loop := NewSubAgentLoop(&partialThenHangProvider{}, tools.NewEmptyRegistry(), "test-model", "sys", SubAgentConfig{
		MaxLoopCycles: 1,
		MaxToolTurns:  1,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	got, err := loop.Run(ctx, "answer me")
	if err == nil {
		t.Fatal("expected a deadline error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want DeadlineExceeded", err)
	}
	if got != "almost done" {
		t.Errorf("response = %q, want the partial streamed text", got)
	}
	for _, m := range loop.State.Messages {
		if m.Role == "assistant" && m.Content == "almost done" {
			t.Error("a partial response must not be persisted as a completed assistant turn")
		}
	}
}
