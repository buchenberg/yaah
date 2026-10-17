package agent

import (
	"errors"
	"testing"
)

func TestProviderErrorLabelsProviderAndModel(t *testing.T) {
	tests := []struct {
		name string
		cfg  LoopConfig
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &Loop{Config: tt.cfg}
			got := l.providerError(errors.New("boom")).Error()
			if got != tt.want {
				t.Fatalf("providerError() = %q, want %q", got, tt.want)
			}
		})
	}
}
