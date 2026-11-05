package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"

	"github.com/hybridgroup/yzma/pkg/llama"
)

// ggufNCtx is the context window reserved for embedding one input, and the
// decode batch size. Embedding models need no conversation history, so the
// whole window is input room.
const ggufNCtx = 2048

// GGUFEmbedder embeds text in-process from a local GGUF model using yzma's
// prebuilt llama.cpp bindings (purego FFI — CGO_ENABLED=0 stays intact).
// It exists so semantic memory works with no LM Studio or Ollama running
// (yaah-m3pk), reusing the same GGUF weights those tools download.
//
// llama.cpp contexts are not safe for concurrent use, so every embed call
// serializes on mu across the whole tokenize → decode → pool sequence. The
// model and context are created lazily on first use: construction stays
// cheap and a missing library or model surfaces as an error (and via the
// yaah-ka6 startup probe) instead of a panic.
type GGUFEmbedder struct {
	// LibPath is the prebuilt llama.cpp shared library the yzma loader
	// needs (e.g. libllama.so). Empty lets the loader pick its default.
	LibPath string
	// ModelPath is the GGUF model file. Required.
	ModelPath string
	// QueryPrefix and DocumentPrefix are the asymmetric prefixes for
	// nomic-style models ("search_query: "/"search_document: "). Leave
	// empty for symmetric models.
	QueryPrefix    string
	DocumentPrefix string

	mu      sync.Mutex
	embedFn func(ctx context.Context, text string) (Embedding, error)
	dims    int
	initErr error
}

// NewGGUFEmbedder returns a GGUF-backed embedder. Model loading is deferred
// to the first Embed call; probe it via the yaah-ka6 startup check.
func NewGGUFEmbedder(libPath, modelPath, queryPrefix, documentPrefix string) *GGUFEmbedder {
	return &GGUFEmbedder{
		LibPath:        libPath,
		ModelPath:      modelPath,
		QueryPrefix:    queryPrefix,
		DocumentPrefix: documentPrefix,
	}
}

// Embed embeds document text, applying the document prefix.
func (g *GGUFEmbedder) Embed(ctx context.Context, text string) (Embedding, error) {
	return g.embedPrefixed(ctx, g.DocumentPrefix, text)
}

// EmbedQuery embeds search-query text, applying the query prefix. It
// satisfies QueryEmbedder so search and storage can be treated
// asymmetrically (nomic-style models score meaningfully higher with
// prefixed query/document sides).
func (g *GGUFEmbedder) EmbedQuery(ctx context.Context, text string) (Embedding, error) {
	return g.embedPrefixed(ctx, g.QueryPrefix, text)
}

// Dims reports the model's embedding width (0 until the model is loaded).
func (g *GGUFEmbedder) Dims() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.dims
}

func (g *GGUFEmbedder) embedPrefixed(ctx context.Context, prefix, text string) (Embedding, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	// Honor cancellation before paying the lazy model load (m3pk review):
	// a probe that already timed out must not kick off a full load.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := g.ensureLocked(ctx); err != nil {
		return nil, err
	}
	emb, err := g.embedFn(ctx, prefix+text)
	if err != nil {
		return nil, err
	}
	// Dims-mismatch refusal (yaah-m3pk, motivated by yaah-shq): never hand
	// back a vector whose width differs from the model's. Storing one would
	// poison recall in exactly the silent way shq exists to fix.
	if g.dims > 0 && len(emb) != g.dims {
		return nil, fmt.Errorf("gguf embed: %s returned %d-dim vector, model is %d-dim — refusing to store a width that would silently break recall", g.ModelPath, len(emb), g.dims)
	}
	return emb, nil
}

// ensureLocked lazily loads the model. Callers hold mu.
func (g *GGUFEmbedder) ensureLocked(ctx context.Context) error {
	if g.embedFn != nil {
		return nil
	}
	if g.initErr != nil {
		return g.initErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fn, dims, err := loadGGUF(ctx, g.LibPath, g.ModelPath)
	if err != nil {
		// A cancelled or timed-out load is transient: caching it would
		// permanently disable the embedder for the process (m3pk review).
		// Real load failures are sticky; context errors retry next call.
		if isContextErr(err) {
			return err
		}
		g.initErr = err
		return err
	}
	g.embedFn, g.dims = fn, dims
	return nil
}

// isContextErr reports whether err is a context cancellation or deadline
// rather than a real load failure.
func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// llamaOnce guards process-global llama.cpp state (library load + init).
// It is never closed: llama.Close would race any other embedder still in
// use, and process teardown reclaims it.
var llamaOnce sync.Once

// loadGGUF loads the llama.cpp library and GGUF model once per process and
// returns a pooling closure plus the model's embedding width. Context is
// checked between the load stages (the FFI calls themselves are not
// interruptible), so a cancelled request stops before the next one.
func loadGGUF(ctx context.Context, libPath, modelPath string) (func(context.Context, string) (Embedding, error), int, error) {
	if modelPath == "" {
		return nil, 0, fmt.Errorf("gguf embed: model_path is required")
	}
	llamaOnce.Do(func() {
		llama.Load(libPath) // prebuilt llama.cpp dylib/so via purego FFI
		llama.Init()
		llama.LogSet(llama.LogSilent())
	})
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}

	model, err := llama.ModelLoadFromFile(modelPath, llama.ModelDefaultParams())
	if err != nil {
		return nil, 0, fmt.Errorf("gguf embed: load model %s: %w", modelPath, err)
	}
	if err := ctx.Err(); err != nil {
		llama.ModelFree(model)
		return nil, 0, err
	}
	threads := int32(runtime.NumCPU())
	if threads > 8 {
		threads = 8
	}
	params := llama.ContextDefaultParams()
	params.NCtx = ggufNCtx
	params.NBatch = ggufNCtx
	params.Embeddings = 1
	params.NThreads = threads
	params.NThreadsBatch = threads
	// Pooling is the embedder's business (yaah-m3pk). Mean pooling matches
	// the embedding models yaah targets (nomic-embed, bge-m3, all-MiniLM).
	params.PoolingType = llama.PoolingTypeMean
	lctx, err := llama.InitFromModel(model, params)
	if err != nil {
		llama.ModelFree(model)
		return nil, 0, fmt.Errorf("gguf embed: init context: %w", err)
	}
	vocab := llama.ModelGetVocab(model)
	dims := int(llama.ModelNEmbd(model))
	if dims <= 0 {
		llama.Free(lctx)
		llama.ModelFree(model)
		return nil, 0, fmt.Errorf("gguf embed: model %s reports %d embedding dims", modelPath, dims)
	}

	fn := func(ctx context.Context, text string) (Embedding, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		tokens := llama.Tokenize(vocab, text, true, true)
		if len(tokens) == 0 {
			return nil, fmt.Errorf("gguf embed: input produced no tokens")
		}
		// Truncate to the context window instead of failing (m3pk review):
		// llama.Decode rejects batches longer than n_ctx, and a long memory
		// body would otherwise error on every embed. Leading tokens (BOS +
		// opening text) are kept, the usual truncation side.
		if len(tokens) > ggufNCtx {
			tokens = tokens[:ggufNCtx]
		}
		if _, err := llama.Decode(lctx, llama.BatchGetOne(tokens)); err != nil {
			return nil, fmt.Errorf("gguf embed: decode: %w", err)
		}
		embs, err := llama.GetEmbeddingsSeq(lctx, 0, int32(dims))
		if err != nil {
			return nil, fmt.Errorf("gguf embed: pool: %w", err)
		}
		if len(embs) != dims {
			return nil, fmt.Errorf("gguf embed: pooled %d dims, model is %d-dim", len(embs), dims)
		}
		// L2-normalize like the yzma reference example so vectors from
		// this backend are numerically comparable with HTTP backends.
		out := make(Embedding, dims)
		var norm float64
		for i, v := range embs {
			out[i] = v
			norm += float64(v) * float64(v)
		}
		if norm > 0 {
			inv := float32(1 / math.Sqrt(norm))
			for i := range out {
				out[i] *= inv
			}
		}
		return out, nil
	}
	return fn, dims, nil
}
