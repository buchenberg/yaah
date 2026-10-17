package llm

import (
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestSwapToFallbackSwapsIdentity(t *testing.T) {
	c := &Client{
		ProviderName:         "zai",
		FallbackProviderName: "deepseek",
		Model:                "GLM-5.3",
		FallbackModel:        "deepseek-v4-pro",
	}
	req := types.ChatRequest{Model: "GLM-5.3"}

	c.swapToFallback(&req)

	if c.ProviderName != "deepseek" {
		t.Errorf("ProviderName = %q, want deepseek", c.ProviderName)
	}
	if c.FallbackProviderName != "zai" {
		t.Errorf("FallbackProviderName = %q, want zai", c.FallbackProviderName)
	}
	if c.Model != "deepseek-v4-pro" {
		t.Errorf("Model = %q, want deepseek-v4-pro", c.Model)
	}
	if req.Model != "deepseek-v4-pro" {
		t.Errorf("req.Model = %q, want deepseek-v4-pro", req.Model)
	}
	if c.FallbackModel != "GLM-5.3" {
		t.Errorf("FallbackModel = %q, want GLM-5.3", c.FallbackModel)
	}
}
