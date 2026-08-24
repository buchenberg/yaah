package yaah

import (
	"testing"

	"github.com/buchenberg/yaah/internal/config"
)

// TestEmbedderFor_fieldMapping pins the provider→embedder mapping. This
// call once passed (BaseURL, Model, APIKey) against the
// (baseURL, apiKey, model) signature — swapping the API key and model
// name, silently breaking semantic search (review finding B4).
func TestEmbedderFor_fieldMapping(t *testing.T) {
	t.Setenv("TEST_EMBED_KEY", "sk-secret-123")

	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"lmstudio": {
				BaseURL: "http://127.0.0.1:1234/v1",
				APIKey:  "${TEST_EMBED_KEY}",
				Name:    "LMStudio",
			},
		},
		Embedding: config.EmbeddingConfig{
			Provider: "lmstudio",
			Model:    "nomic-embed-text",
		},
	}

	e, ok := embedderFor(cfg)
	if !ok {
		t.Fatal("embedderFor returned false for a complete config")
	}
	if e.BaseURL != "http://127.0.0.1:1234/v1" {
		t.Errorf("BaseURL = %q", e.BaseURL)
	}
	if e.APIKey != "sk-secret-123" {
		t.Errorf("APIKey = %q; want the resolved env value (env substitution or field swap regression)", e.APIKey)
	}
	if e.Model != "nomic-embed-text" {
		t.Errorf("Model = %q; want nomic-embed-text (model/api-key swap regression)", e.Model)
	}
}

func TestEmbedderFor_incomplete(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
	}{
		{"no provider name", &config.Config{
			Providers: map[string]config.Provider{"x": {}},
			Embedding: config.EmbeddingConfig{Model: "m"},
		}},
		{"no model", &config.Config{
			Providers: map[string]config.Provider{"x": {}},
			Embedding: config.EmbeddingConfig{Provider: "x"},
		}},
		{"unknown provider", &config.Config{
			Providers: map[string]config.Provider{"x": {}},
			Embedding: config.EmbeddingConfig{Provider: "nope", Model: "m"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := embedderFor(tt.cfg); ok {
				t.Error("embedderFor returned true for incomplete config")
			}
		})
	}
}
