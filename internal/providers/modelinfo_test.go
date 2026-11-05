package providers

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/providers/catalog"
)

func TestResolveWindow(t *testing.T) {
	t.Run("known model", func(t *testing.T) {
		got := ResolveWindow("deepseek-v4-pro", 0)
		if got != 1048576 {
			t.Errorf("ResolveWindow(deepseek-v4-pro, 0) = %d, want 1048576", got)
		}
	})

	t.Run("known model with provider prefix", func(t *testing.T) {
		got := ResolveWindow("deepseek/deepseek-v4-pro", 0)
		if got != 1048576 {
			t.Errorf("ResolveWindow(deepseek/deepseek-v4-pro, 0) = %d, want 1048576", got)
		}
	})

	t.Run("known model with different provider prefix", func(t *testing.T) {
		got := ResolveWindow("openai/gpt-4o", 0)
		if got != 128000 {
			t.Errorf("ResolveWindow(openai/gpt-4o, 0) = %d, want 128000", got)
		}
	})

	t.Run("config cap is smaller and wins", func(t *testing.T) {
		got := ResolveWindow("deepseek-v4-pro", 32000)
		if got != 32000 {
			t.Errorf("ResolveWindow(deepseek-v4-pro, 32000) = %d, want 32000", got)
		}
	})

	t.Run("config cap is larger, discovered wins", func(t *testing.T) {
		got := ResolveWindow("deepseek-v4-pro", 2000000)
		if got != 1048576 {
			t.Errorf("ResolveWindow(deepseek-v4-pro, 2000000) = %d, want 1048576", got)
		}
	})

	t.Run("unknown model falls back to config", func(t *testing.T) {
		got := ResolveWindow("unknown-model-42", 65536)
		if got != 65536 {
			t.Errorf("ResolveWindow(unknown-model-42, 65536) = %d, want 65536", got)
		}
	})

	t.Run("unknown model with prefix falls back to config", func(t *testing.T) {
		got := ResolveWindow("local/unknown-model-42", 65536)
		if got != 65536 {
			t.Errorf("ResolveWindow(local/unknown-model-42, 65536) = %d, want 65536", got)
		}
	})

	t.Run("zero config cap returns discovered", func(t *testing.T) {
		got := ResolveWindow("gpt-4o", 0)
		if got != 128000 {
			t.Errorf("ResolveWindow(gpt-4o, 0) = %d, want 128000", got)
		}
	})

	t.Run("claude sonnet", func(t *testing.T) {
		got := ResolveWindow("claude-3.5-sonnet", 0)
		if got != 200000 {
			t.Errorf("ResolveWindow(claude-3.5-sonnet, 0) = %d, want 200000", got)
		}
	})

	t.Run("gemini large context", func(t *testing.T) {
		got := ResolveWindow("gemini-2.5-pro", 0)
		if got != 1048576 {
			t.Errorf("ResolveWindow(gemini-2.5-pro, 0) = %d, want 1048576", got)
		}
	})
}

func TestSanitizeModelName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"deepseek-v4-pro", "deepseek-v4-pro"},
		{"deepseek/deepseek-v4-pro", "deepseek-v4-pro"},
		{"openai/gpt-4o", "gpt-4o"},
		{"anthropic/claude-3.5-sonnet", "claude-3.5-sonnet"},
		{"provider/with/many/slashes", "slashes"},
	}
	for _, tc := range tests {
		got := sanitizeModelName(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeModelName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestIsThinkingModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		// Thinking models: reasoning_content must be passed back on every
		// assistant message or the provider rejects the request.
		{"deepseek/deepseek-flash", true},
		{"deepseek-flash", true},
		{"deepseek-r1", true},
		{"deepseek-r1-0528", true},
		{"deepseek/deepseek-v4-flash", true},
		{"o1", true},
		{"o3-mini", true},

		// Non-thinking models: reasoning_content is omitted.
		{"deepseek-v3", false},
		{"deepseek/deepseek-chat", false},
		{"gpt-4o", false},
		{"claude-3.5-sonnet", false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := IsThinkingModel(tt.model); got != tt.want {
				t.Errorf("IsThinkingModel(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestResolveWindowDeepseekFlash(t *testing.T) {
	if got := ResolveWindow("deepseek/deepseek-flash", 0); got != 1048576 {
		t.Errorf("ResolveWindow(deepseek/deepseek-flash, 0) = %d, want 1048576", got)
	}
}

// Resolution precedence is config > hand registry > models.dev snapshot >
// defaults. These tests pin the order rather than the numbers, so regenerating
// the snapshot does not invalidate them.
func TestResolveWindowPrecedence(t *testing.T) {
	t.Run("config cap beats the hand registry", func(t *testing.T) {
		if got := ResolveWindow("deepseek-v4-pro", 32000); got != 32000 {
			t.Errorf("ResolveWindow(deepseek-v4-pro, 32000) = %d, want the 32000 config cap", got)
		}
	})

	t.Run("hand registry beats the snapshot", func(t *testing.T) {
		// deepseek-v3 is deliberately conservatively sized at 65536 in the
		// hand table; the snapshot publishes a wider window.
		want := modelWindows["deepseek-v3"]
		if got := ResolveWindow("deepseek-v3", 0); got != want {
			t.Errorf("ResolveWindow(deepseek-v3, 0) = %d, want the hand value %d", got, want)
		}
		if snap, ok := catalog.ContextWindow("deepseek-v3"); ok && snap != want {
			t.Logf("snapshot offers %d for deepseek-v3; hand value %d still wins", snap, want)
		}
	})

	t.Run("snapshot answers for models the hand table lacks", func(t *testing.T) {
		const model = "claude-haiku-4-5"
		if _, ok := modelWindows[model]; ok {
			t.Fatalf("%q is now in modelWindows; pick another snapshot-only model", model)
		}
		want, ok := catalog.ContextWindow(model)
		if !ok {
			t.Skipf("snapshot no longer knows %q", model)
		}
		if got := ResolveWindow(model, 0); got != want {
			t.Errorf("ResolveWindow(%q, 0) = %d, want the snapshot value %d", model, got, want)
		}
		if got := ResolveWindow(model, want/2); got != want/2 {
			t.Errorf("ResolveWindow(%q, %d) = %d, want the config cap to win", model, want/2, got)
		}
	})

	t.Run("unknown everywhere returns the config default", func(t *testing.T) {
		if got := ResolveWindow("totally-unknown-model-42", 65536); got != 65536 {
			t.Errorf("ResolveWindow(unknown, 65536) = %d, want 65536", got)
		}
	})
}

func TestResolveOutputLimitPrecedence(t *testing.T) {
	t.Run("hand table beats the published ceiling", func(t *testing.T) {
		// The conservative table caps deepseek-flash at 32768 while
		// models.dev publishes a much larger limit.output.
		for _, model := range []string{"deepseek-flash", "deepseek-v4-pro"} {
			want := modelOutputLimits[model]
			if got := ResolveOutputLimit(model); got != want {
				t.Errorf("ResolveOutputLimit(%q) = %d, want the hand value %d", model, got, want)
			}
		}
	})

	t.Run("alias resolves onto the hand entry", func(t *testing.T) {
		// Dotted family → hyphenated hand key, still with the provider
		// prefix that normalization strips.
		if got := ResolveOutputLimit("anthropic/claude-3.5-sonnet"); got != 8192 {
			t.Errorf("ResolveOutputLimit(anthropic/claude-3.5-sonnet) = %d, want 8192", got)
		}
	})

	t.Run("snapshot answers for models outside the table", func(t *testing.T) {
		const model = "claude-haiku-4-5"
		if handOutputLimit(normalizeModelID(model)) > 0 {
			t.Fatalf("%q now resolves in the hand table; pick another snapshot-only model", model)
		}
		want, ok := catalog.OutputLimit(model)
		if !ok {
			t.Skipf("snapshot no longer knows %q", model)
		}
		if got := ResolveOutputLimit(model); got != want {
			t.Errorf("ResolveOutputLimit(%q) = %d, want the snapshot value %d", model, got, want)
		}
	})

	t.Run("unknown model returns zero", func(t *testing.T) {
		if got := ResolveOutputLimit("totally-unknown-model-42"); got != 0 {
			t.Errorf("ResolveOutputLimit(unknown) = %d, want 0", got)
		}
	})
}

func TestIsThinkingModelPrecedence(t *testing.T) {
	t.Run("hand prefixes beat the snapshot", func(t *testing.T) {
		// "deepseek-v4" is a hand prefix, so any variant is thinking even
		// when the snapshot has never heard of the exact ID.
		if !IsThinkingModel("deepseek-v4-anything") {
			t.Error("IsThinkingModel(deepseek-v4-anything) = false, want the hand prefix match")
		}
	})

	t.Run("snapshot fills the long tail", func(t *testing.T) {
		const model = "claude-haiku-4-5"
		if thinkingModels[model] {
			t.Fatalf("%q is now in thinkingModels; pick another snapshot-only model", model)
		}
		for _, prefix := range thinkingPrefixes {
			if strings.HasPrefix(model, prefix) {
				t.Fatalf("%q matches hand prefix %q; pick another model", model, prefix)
			}
		}
		reasoning, known := catalog.Reasoning(model)
		if !known || !reasoning {
			t.Skipf("snapshot no longer flags %q as reasoning", model)
		}
		if !IsThinkingModel(model) {
			t.Errorf("IsThinkingModel(%q) = false, want the snapshot's reasoning flag", model)
		}
	})

	t.Run("snapshot false is not overridden by anything", func(t *testing.T) {
		if IsThinkingModel("gpt-4o") {
			t.Error("IsThinkingModel(gpt-4o) = true, want false")
		}
	})

	t.Run("unknown model defaults to non-thinking", func(t *testing.T) {
		if IsThinkingModel("totally-unknown-model-42") {
			t.Error("IsThinkingModel(unknown) = true, want false")
		}
	})
}

// Config still wins over the snapshot in both directions.
func TestResolveThinkingMode_configOverridesSnapshot(t *testing.T) {
	no := false
	c := &OpenAIClient{ThinkingOverrides: map[string]*bool{"claude-haiku-4-5": &no}}
	if c.resolveThinkingMode("claude-haiku-4-5") {
		t.Error("resolveThinkingMode(claude-haiku-4-5) = true, want the config override to win")
	}
}

func TestResolveThinkingMode_configOverridesRegistry(t *testing.T) {
	yes, no := true, false
	c := &OpenAIClient{ThinkingOverrides: map[string]*bool{
		"deepseek-v3":    &yes, // registry says non-thinking
		"deepseek-flash": &no,  // registry says thinking
	}}

	tests := []struct {
		name  string
		model string
		want  bool
	}{
		{"override forces thinking on a non-thinking model", "deepseek/deepseek-v3", true},
		{"override forces thinking off a thinking model", "deepseek/deepseek-flash", false},
		{"no override — registry thinking model", "deepseek-r1", true},
		{"no override — registry prefix match", "deepseek-v4-anything", true},
		{"no override — unknown model defaults off", "gpt-4o", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := c.resolveThinkingMode(tt.model); got != tt.want {
				t.Errorf("resolveThinkingMode(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}
