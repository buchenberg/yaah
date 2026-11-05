package config

// EmbeddingSearchLive reports whether semantic memory search is actually
// enabled. This is the single enablement predicate: embedderFor gates on
// it, the TUI info pane displays it, and `yaah config` reports it, so the
// status display cannot drift from runtime behavior (the gguf backend
// once rendered "(inactive)" while search ran, because the display
// checked provider+model only).
//
// The gguf backend is live from embedding.model_path alone; the http
// backend needs both embedding.provider and embedding.model with the
// provider resolving in the providers map; any other backend is not live.
func (c Config) EmbeddingSearchLive() bool {
	switch {
	case c.Embedding.Backend == EmbeddingBackendGGUF:
		return c.Embedding.ModelPath != ""
	case c.Embedding.Backend != "" && c.Embedding.Backend != EmbeddingBackendHTTP:
		return false
	case c.Embedding.Provider == "" || c.Embedding.Model == "":
		return false
	}
	_, ok := c.Providers[c.Embedding.Provider]
	return ok
}
