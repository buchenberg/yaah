package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

// TestTurnErrorAttributesExpiredBudget: when the run context is done, the
// provider was only in flight — the failure belongs to the deadline/cancel
// path, not to the provider. Mislabeling it sends operators chasing z.ai.
func TestTurnErrorAttributesExpiredBudget(t *testing.T) {
	l := &Loop{Config: LoopConfig{ProviderName: "zai", Model: "GLM-5.3-Flash"}}

	t.Run("deadline exceeded", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		<-ctx.Done()

		got := l.turnError(ctx, errors.New("provider boom"))
		if !errors.Is(got, context.DeadlineExceeded) {
			t.Fatalf("turnError() = %v, want context.DeadlineExceeded", got)
		}
		if errors.Is(got, context.Canceled) {
			t.Errorf("turnError() = %v, must not look like a cancellation", got)
		}
		if !strings.Contains(got.Error(), "provider boom") {
			t.Errorf("turnError() = %v, want the underlying provider cause preserved", got)
		}
	})

	t.Run("ready context keeps provider attribution", func(t *testing.T) {
		got := l.turnError(context.Background(), errors.New("boom")).Error()
		if want := "provider error [zai/GLM-5.3-Flash]: boom"; got != want {
			t.Fatalf("turnError() = %q, want %q", got, want)
		}
	})
}
