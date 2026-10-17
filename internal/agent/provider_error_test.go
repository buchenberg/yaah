package agent

import (
	"errors"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/llm"
)

func TestProviderErrorLabelsProviderAndModel(t *testing.T) {
	tests := []struct {
		name string
		cfg  LoopConfig
		llm  *llm.Client
		want string
	}{
		{
			name: "provider and model",
			cfg:  LoopConfig{ProviderName: "zai", Model: "GLM-5.3-Flash"},
			want: "provider error [zai/GLM-5.3-Flash]: boom",
		},
		{
			name: "model only",
			cfg:  LoopConfig{Model: "GLM-5.3-Flash"},
			want: "provider error [GLM-5.3-Flash]: boom",
		},
		{
			name: "provider only",
			cfg:  LoopConfig{ProviderName: "zai"},
			want: "provider error [zai]: boom",
		},
		{
			name: "no identity",
			cfg:  LoopConfig{},
			want: "provider error: boom",
		},
		{
			name: "live fallback identity wins over stale config",
			cfg:  LoopConfig{ProviderName: "zai", Model: "GLM-5.3"},
			llm:  &llm.Client{ProviderName: "deepseek", Model: "deepseek-v4-pro"},
			want: "provider error [deepseek/deepseek-v4-pro]: boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &Loop{Config: tt.cfg, LLM: tt.llm}
			got := l.providerError(errors.New("boom")).Error()
			if got != tt.want {
				t.Fatalf("providerError() = %q, want %q", got, tt.want)
			}
		})
	}
}
