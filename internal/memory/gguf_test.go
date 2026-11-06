package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeGGUF wires a GGUFEmbedder to a stub pooling closure so prefix and
// dims behavior is testable without a real GGUF model or llama.cpp lib.
func fakeGGUF(dims int, fn func(ctx context.Context, text string) (Embedding, error)) *GGUFEmbedder {
	g := NewGGUFEmbedder("", "model.gguf", "search_query: ", "search_document: ")
	g.dims = dims
	g.embedFn = fn
	return g
}

func TestGGUF_EmbedAppliesDocumentPrefix(t *testing.T) {
	var got []string
	g := fakeGGUF(3, func(_ context.Context, text string) (Embedding, error) {
		got = append(got, text)
		return Embedding{1, 0, 0}, nil
	})
	if _, err := g.Embed(context.Background(), "fact"); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if _, err := g.EmbedQuery(context.Background(), "query"); err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}
	want := []string{"search_document: fact", "search_query: query"}
	if len(got) != len(want) {
		t.Fatalf("got %d embed calls %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("embed call %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestGGUF_EmbedQuerySatisfiesQueryEmbedder(t *testing.T) {
	g := fakeGGUF(2, func(_ context.Context, text string) (Embedding, error) {
		return Embedding{1, 2}, nil
	})
	q, ok := Embedder(g).(QueryEmbedder)
	if !ok {
		t.Fatal("*GGUFEmbedder must satisfy QueryEmbedder")
	}
	emb, err := q.EmbedQuery(context.Background(), "x")
	if err != nil || len(emb) != 2 {
		t.Fatalf("EmbedQuery = %v, %v; want 2-dim, nil", emb, err)
	}
}

// yaah-m3pk: a vector whose width differs from the model's must be refused
// rather than stored — silent width mixing is the yaah-shq failure mode.
func TestGGUF_DimsMismatchRefused(t *testing.T) {
	g := fakeGGUF(4, func(_ context.Context, text string) (Embedding, error) {
		return Embedding{1, 2, 3}, nil // wrong width on purpose
	})
	_, err := g.Embed(context.Background(), "fact")
	if err == nil {
		t.Fatal("Embed must refuse a vector whose width differs from the model's")
	}
	if !strings.Contains(err.Error(), "refusing") {
		t.Errorf("error should say it refuses the vector, got: %v", err)
	}
}

// yaah-m3pk review: a cancelled/timed-out load must not be cached — one
// transient timeout would otherwise disable the embedder for the whole
// process, silently degrading semantic search to keyword-only.
func TestGGUF_CancelledLoadNotCached(t *testing.T) {
	g := NewGGUFEmbedder("", "model.gguf", "", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.ensureLocked(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ensureLocked(cancelled) = %v, want context.Canceled", err)
	}
	if g.initErr != nil {
		t.Fatalf("cancellation cached as initErr %v; embedder would be permanently disabled", g.initErr)
	}
}

func TestGGUF_IsContextErr(t *testing.T) {
	if !isContextErr(context.Canceled) || !isContextErr(context.DeadlineExceeded) {
		t.Error("isContextErr must recognize both context errors")
	}
	if isContextErr(errors.New("model corrupt")) {
		t.Error("isContextErr must not classify real load failures as transient")
	}
}

func TestGGUF_EmptyModelPathRefused(t *testing.T) {
	g := NewGGUFEmbedder("", "", "", "")
	_, err := g.Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("Embed must fail when model_path is empty")
	}
}

func TestGGUF_InitErrorIsSticky(t *testing.T) {
	g := &GGUFEmbedder{ModelPath: "missing.gguf", initErr: errors.New("boom")}
	for i := 0; i < 2; i++ {
		if _, err := g.Embed(context.Background(), "x"); err == nil || err.Error() != "boom" {
			t.Fatalf("call %d: err = %v, want boom", i, err)
		}
	}
}

// plainEmbedder is a bare Embedder with no query-side treatment.
type plainEmbedder struct{}

func (plainEmbedder) Embed(context.Context, string) (Embedding, error) {
	return Embedding{0}, nil
}

func TestEmbedQuery_FallsBackToEmbed(t *testing.T) {
	var e Embedder = plainEmbedder{}
	if _, ok := e.(QueryEmbedder); ok {
		t.Fatal("plainEmbedder must not satisfy QueryEmbedder")
	}
	if _, err := EmbedQuery(context.Background(), e, "q"); err != nil {
		t.Fatalf("embedQuery must fall back to Embed, got: %v", err)
	}
}
