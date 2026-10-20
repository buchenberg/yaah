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

func TestOpenAIClientEffectiveMaxTokens(t *testing.T) {
	client := NewOpenAIClient("http://localhost:1234/v1", "key", 0)
	client.DefaultMaxTokens = 12345

	if got := client.EffectiveMaxTokens(types.ChatRequest{Model: "m"}); got != 12345 {
		t.Errorf("unset request: EffectiveMaxTokens = %d, want provider default 12345", got)
	}
	if got := client.EffectiveMaxTokens(types.ChatRequest{Model: "m", MaxTokens: 4096}); got != 4096 {
		t.Errorf("explicit cap: EffectiveMaxTokens = %d, want 4096", got)
	}

	// Zero means the server owns the cap; the retry ladder skips the raise
	// when this reports 0.
	client.DefaultMaxTokens = 0
	if got := client.EffectiveMaxTokens(types.ChatRequest{Model: "m"}); got != 0 {
		t.Errorf("no configured cap: EffectiveMaxTokens = %d, want 0", got)
	}
	if got := client.OutputTokenCeiling(types.ChatRequest{Model: "m"}); got != 0 {
		t.Errorf("OutputTokenCeiling = %d, want 0 (unknown)", got)
	}
	if got := client.OutputTokenCeiling(types.ChatRequest{Model: "gpt-4o"}); got != 16384 {
		t.Errorf("gpt-4o OutputTokenCeiling = %d, want 16384", got)
	}
}
