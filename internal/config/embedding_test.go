package config

import "testing"

// TestEmbeddingSearchLive pins the single enablement predicate shared by
// embedderFor, the TUI info pane, and `yaah config`. Each case here is
// also asserted against embedderFor in
// TestEmbedderFor_matchesEmbeddingSearchLive so the display cannot drift
// from runtime behavior.
func TestEmbeddingSearchLive(t *testing.T) {
	live := map[string]Provider{"p": {}}
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"gguf with model_path", Config{Embedding: EmbeddingConfig{
			Backend:   EmbeddingBackendGGUF,
			ModelPath: "/models/nomic.gguf",
		}}, true},
		{"gguf without model_path", Config{Embedding: EmbeddingConfig{
			Backend: EmbeddingBackendGGUF,
		}}, false},
		{"http complete", Config{Providers: live, Embedding: EmbeddingConfig{
			Provider: "p",
			Model:    "m",
		}}, true},
		{"explicit http complete", Config{Providers: live, Embedding: EmbeddingConfig{
			Backend:  EmbeddingBackendHTTP,
			Provider: "p",
			Model:    "m",
		}}, true},
		{"http dangling provider", Config{Providers: live, Embedding: EmbeddingConfig{
			Provider: "nope",
			Model:    "m",
		}}, false},
		{"http half-set provider only", Config{Embedding: EmbeddingConfig{
			Provider: "p",
		}}, false},
		{"http half-set model only", Config{Embedding: EmbeddingConfig{
			Model: "m",
		}}, false},
		{"unknown backend", Config{Providers: live, Embedding: EmbeddingConfig{
			Backend:  "bogus",
			Provider: "p",
			Model:    "m",
		}}, false},
		{"nothing configured", Config{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.EmbeddingSearchLive(); got != tt.want {
				t.Errorf("EmbeddingSearchLive() = %v, want %v", got, tt.want)
			}
		})
	}
}
