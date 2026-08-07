package providers

import (
	"context"
	"fmt"

	"github.com/buchenberg/yaah/internal/types"
)

// NoProviderStub is returned when no valid provider is configured.
// It satisfies the provider interface but returns a helpful error
// on every Send call.
type NoProviderStub struct{}

// Send implements the Provider interface with an error directing the
// user to configure a provider.
func (s *NoProviderStub) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	return nil, fmt.Errorf("no provider configured — run 'yaah config edit' to add one")
}

// OAuthErrorStub is returned when an OAuth provider has no stored token.
// It satisfies the provider interface but returns an authentication error
// on every Send call.
type OAuthErrorStub struct {
	Provider string
	Err      error
}

// Send implements the Provider interface with an OAuth authentication error.
func (s *OAuthErrorStub) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	if s.Err != nil {
		return nil, fmt.Errorf("provider %q: %w", s.Provider, s.Err)
	}
	return nil, fmt.Errorf("provider %q not authenticated — run 'yaah login %s'", s.Provider, s.Provider)
}
