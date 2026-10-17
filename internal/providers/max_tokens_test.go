package providers

import (
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestOpenAIClientDefaultMaxTokens(t *testing.T) {
	client := NewOpenAIClient("http://localhost:1234/v1", "key", 0)
	client.DefaultMaxTokens = 12345

	req := types.ChatRequest{Model: "m"}
	client.applyDefaultMaxTokens(&req)
	if req.MaxTokens != 12345 {
		t.Errorf("expected provider default 12345, got %d", req.MaxTokens)
	}

	req.MaxTokens = 4096
	client.applyDefaultMaxTokens(&req)
	if req.MaxTokens != 4096 {
		t.Errorf("explicit cap must win, got %d", req.MaxTokens)
	}
}
