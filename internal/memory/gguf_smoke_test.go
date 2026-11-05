package memory

import (
	"context"
	"os"
	"testing"
)

// Manual smoke test for the local gguf setup (run with YAAH_GGUF_SMOKE=1).
func TestGGUFSmoke(t *testing.T) {
	if os.Getenv("YAAH_GGUF_SMOKE") == "" {
		t.Skip("set YAAH_GGUF_SMOKE=1 to run")
	}
	e := NewGGUFEmbedder(os.Getenv("YAAH_GGUF_LIB"), os.Getenv("YAAH_GGUF_MODEL"), "search_query: ", "search_document: ")
	emb, err := e.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	t.Logf("dims=%d first3=%v", len(emb), emb[:3])
	q, err := e.EmbedQuery(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}
	if len(q) != len(emb) {
		t.Errorf("query dims %d != doc dims %d", len(q), len(emb))
	}
}
