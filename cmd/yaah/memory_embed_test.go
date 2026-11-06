package yaah

import (
	"testing"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/memory"
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
			Provider:       "lmstudio",
			Model:          "nomic-embed-text",
			QueryPrefix:    "search_query: ",
			DocumentPrefix: "search_document: ",
		},
	}

	e, ok := embedderFor(cfg)
	if !ok {
		t.Fatal("embedderFor returned false for a complete config")
	}
	h, ok := e.(*memory.HTTPEmbedder)
	if !ok {
		t.Fatalf("embedderFor returned %T, want *memory.HTTPEmbedder", e)
	}
	if h.BaseURL != "http://127.0.0.1:1234/v1" {
		t.Errorf("BaseURL = %q", h.BaseURL)
	}
	if h.APIKey != "sk-secret-123" {
		t.Errorf("APIKey = %q; want the resolved env value (env substitution or field swap regression)", h.APIKey)
	}
	if h.Model != "nomic-embed-text" {
		t.Errorf("Model = %q; want nomic-embed-text (model/api-key swap regression)", h.Model)
	}
	if h.QueryPrefix != "search_query: " || h.DocumentPrefix != "search_document: " {
		t.Errorf("prefixes = %q/%q, want the configured nomic prefixes (http path must carry them too)", h.QueryPrefix, h.DocumentPrefix)
	}
}

// TestEmbedderFor_ggufRouting pins the yaah-m3pk backend dispatch: gguf
// yields an in-process *memory.GGUFEmbedder without any provider, and a
// gguf config missing model_path is refused with a warning.
func TestEmbedderFor_ggufRouting(t *testing.T) {
	e, ok := embedderFor(&config.Config{
		Embedding: config.EmbeddingConfig{
			Backend:        config.EmbeddingBackendGGUF,
			ModelPath:      "/models/nomic-embed.gguf",
			LibPath:        "/lib/libllama.so",
			QueryPrefix:    "search_query: ",
			DocumentPrefix: "search_document: ",
		},
	})
	if !ok {
		t.Fatal("embedderFor returned false for a complete gguf config")
	}
	g, ok := e.(*memory.GGUFEmbedder)
	if !ok {
		t.Fatalf("embedderFor returned %T, want *memory.GGUFEmbedder", e)
	}
	if g.ModelPath != "/models/nomic-embed.gguf" {
		t.Errorf("ModelPath = %q", g.ModelPath)
	}
	if g.LibPath != "/lib/libllama.so" {
		t.Errorf("LibPath = %q", g.LibPath)
	}
	if g.QueryPrefix != "search_query: " || g.DocumentPrefix != "search_document: " {
		t.Errorf("prefixes = %q/%q", g.QueryPrefix, g.DocumentPrefix)
	}

	if _, ok := embedderFor(&config.Config{
		Embedding: config.EmbeddingConfig{Backend: config.EmbeddingBackendGGUF},
	}); ok {
		t.Error("embedderFor must refuse gguf without model_path")
	}
	if _, ok := embedderFor(&config.Config{
		Embedding: config.EmbeddingConfig{Backend: "bogus"},
	}); ok {
		t.Error("embedderFor must refuse an unknown backend")
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

// TestEmbedderFor_matchesEmbeddingSearchLive pins that the enablement
// predicate shared with the TUI info pane and `yaah config` is exactly
// embedderFor's own: if the status display ever disagrees with runtime
// behavior again (the gguf backend once rendered "(inactive)" while
// semantic search ran), this fails.
func TestEmbedderFor_matchesEmbeddingSearchLive(t *testing.T) {
	cfgs := map[string]*config.Config{
		"gguf with model_path": {Embedding: config.EmbeddingConfig{
			Backend: config.EmbeddingBackendGGUF, ModelPath: "/models/nomic.gguf",
		}},
		"gguf without model_path": {Embedding: config.EmbeddingConfig{
			Backend: config.EmbeddingBackendGGUF,
		}},
		"http complete": {
			Providers: map[string]config.Provider{"p": {}},
			Embedding: config.EmbeddingConfig{Provider: "p", Model: "m"},
		},
		"http explicit backend": {
			Providers: map[string]config.Provider{"p": {}},
			Embedding: config.EmbeddingConfig{Backend: config.EmbeddingBackendHTTP, Provider: "p", Model: "m"},
		},
		"http dangling provider": {
			Providers: map[string]config.Provider{"p": {}},
			Embedding: config.EmbeddingConfig{Provider: "nope", Model: "m"},
		},
		"http half-set provider only": {Embedding: config.EmbeddingConfig{Provider: "p"}},
		"http half-set model only":    {Embedding: config.EmbeddingConfig{Model: "m"}},
		"unknown backend": {
			Providers: map[string]config.Provider{"p": {}},
			Embedding: config.EmbeddingConfig{Backend: "bogus", Provider: "p", Model: "m"},
		},
		"nothing configured": {},
	}
	for name, cfg := range cfgs {
		t.Run(name, func(t *testing.T) {
			_, ok := embedderFor(cfg)
			if ok != cfg.EmbeddingSearchLive() {
				t.Errorf("embedderFor ok = %v, EmbeddingSearchLive = %v; the display must match runtime", ok, cfg.EmbeddingSearchLive())
			}
		})
	}
}
