package llm

import (
	"context"

	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/types"
)

// Provider is the interface for synchronous model backends.
type Provider interface {
	Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error)
}

// StreamProvider is a provider that supports streaming responses.
type StreamProvider interface {
	Provider
	SendStream(ctx context.Context, req types.ChatRequest) (<-chan providers.StreamChunk, <-chan error)
}

// TokenCallback is called for each streamed token.
type TokenCallback func(token string)

// ThinkingCallback is called when the model outputs reasoning text.
type ThinkingCallback func(text string)

// CompactFunc is called to reduce context size when token overflow is detected.
type CompactFunc func(ctx context.Context, messages []types.Message, threshold float64) []types.Message
