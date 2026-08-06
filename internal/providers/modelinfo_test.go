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
