package providers

import "testing"

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

// Config is the documented source of truth for thinking mode: a per-model
// `thinking` override must beat the built-in registry in BOTH directions, and
// the registry only supplies a default when config is silent.
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
