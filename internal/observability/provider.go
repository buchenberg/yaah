package observability

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/types"
)

// LLMProvider is the subset of agent.Provider + agent.StreamProvider
// that InstrumentedProvider delegates to. It avoids importing the
// agent package directly so the dependency is one-way.
type LLMProvider interface {
	Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error)
	SendStream(ctx context.Context, req types.ChatRequest) (<-chan providers.StreamChunk, <-chan error)
}

// InstrumentedProvider wraps an LLMProvider with OpenTelemetry spans
// on every non-streaming Send call. Streaming calls pass through
// directly since the stream lifetime is managed by the agent loop.
type InstrumentedProvider struct {
	Inner LLMProvider
}

// Send wraps the inner Send call in an "llm.send" span with duration
// and token usage attributes.
func (p *InstrumentedProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	ctx, span := StartLLM(ctx, req.Model)
	defer span.End()
	start := time.Now()

	resp, err := p.Inner.Send(ctx, req)
	dur := time.Since(start)
	span.SetAttributes(attribute.Int64("llm.duration_ms", dur.Milliseconds()))

	if err != nil {
		RecordError(span, err)
		return nil, err
	}

	sysLen := 0
	for _, m := range req.Messages {
		if m.Role == "system" {
			sysLen += len(m.Content)
		}
	}
	FinishLLM(span, len(req.Messages), sysLen, resp.Usage)

	return resp, nil
}

// SendStream passes through to the inner provider.
func (p *InstrumentedProvider) SendStream(ctx context.Context, req types.ChatRequest) (<-chan providers.StreamChunk, <-chan error) {
	return p.Inner.SendStream(ctx, req)
}
