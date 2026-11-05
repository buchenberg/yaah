package memory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
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

	mu           sync.Mutex
	embedFn      func(ctx context.Context, text string) (Embedding, error)
	dims         int
	decodeWindow int
	trainContext int
	initErr      error
}

// DecodeWindow reports the decode window the embedder actually uses —
// tokens per decode after the train-context clamp — or 0 before the
// model is loaded. Diagnostic surface for the clamp (yaah-x58j).
func (g *GGUFEmbedder) DecodeWindow() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.decodeWindow
}

// TrainContext reports the model's own n_ctx_train (the clamp's source),
// or 0 when the model is not yet loaded — which is also what a model
// that reports no n_ctx_train returns, so "unloaded" and "no train
// context reported" are not distinguishable through this accessor. Once
// loaded, a 0 train context leaves the clamped window at the reserved
// default (before any load, DecodeWindow is 0 too — nothing is clamped
// or resolved yet).
func (g *GGUFEmbedder) TrainContext() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.trainContext
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
	fn, dims, ctxWindow, trainContext, err := loadGGUF(ctx, g.LibPath, g.ModelPath)
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
	g.embedFn, g.dims, g.decodeWindow, g.trainContext = fn, dims, ctxWindow, trainContext
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

// llamaLoadErr holds the once-guard's failure: the library stages run
// exactly once per process, so a failure must be remembered on shared
// state — later embed callers (a second embedder, the startup probe over
// the live path) must see the same error instead of skipping into unbound
// FFI. Reading it after llamaOnce.Do returns is safe: Do guarantees the
// closure's memory effects are visible to every caller on return.
var llamaLoadErr error

// llamaLogMu/llamaLogFile back the level-aware log hook: ggml noise
// (the backend-scan chatter) is dropped, but WARN/ERROR land in a file —
// never the tty, and never lost: a silenced GGML abort is a crash with
// no diagnosis (the SIGABRT-in-decode of yaah-x58j printed its assert
// text into the silent callback).
var llamaLogMu sync.Mutex
var llamaLogFile *os.File

// llamaLogHook installs the llama.cpp/ggml log callback. DEBUG/INFO noise
// (e.g. "load_backend: loaded CPU backend from …" — the tty artifact's
// original source) is dropped; WARN/ERROR append to ~/.yaah/llama.log so
// field failures (a GGML_ASSERT abort among them) stay diagnosable.
func llamaLogHook() {
	// The same home resolution the rest of the package uses (YAAH_HOME
	// for sandboxed/relocated homes), with the directory created — an
	// ENOENT on ~/.yaah would otherwise silently drop every diagnostic.
	home := os.Getenv("YAAH_HOME")
	if home == "" {
		userHome, uerr := os.UserHomeDir()
		if uerr != nil {
			llama.LogSet(llama.LogSilent())
			return
		}
		home = filepath.Join(userHome, ".yaah")
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		llama.LogSet(llama.LogSilent())
		return
	}
	f, err := os.OpenFile(filepath.Join(home, "llama.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		// Nowhere to write the diagnostics — drop everything rather than
		// print (printing is the tty artifact).
		llama.LogSet(llama.LogSilent())
		return
	}
	llamaLogFile = f
	cb := purego.NewCallback(func(level int32, text unsafe.Pointer, data uintptr) uintptr {
		// ggml_log_level: none=0 debug=1 info=2 warn=3 error=4 cont=5.
		if level != 3 && level != 4 && level != 5 {
			return 0
		}
		var b []byte
		if text != nil {
			buf := (*[8192]byte)(text)
			if n := bytes.IndexByte(buf[:], 0); n > 0 {
				b = buf[:n]
			}
		}
		if len(b) == 0 {
			return 0
		}
		llamaLogMu.Lock()
		defer llamaLogMu.Unlock()
		if llamaLogFile == nil {
			return 0
		}
		if level == 5 { // cont: continue the previous line
			llamaLogFile.Write(b)
			return 0
		}
		llamaLogFile.Write([]byte(time.Now().Format("2006/01/02 15:04:05 ")))
		llamaLogFile.Write([]byte(llamaLevelName(level) + " " + string(b) + "\n"))
		return 0
	})
	llama.LogSet(cb)
}

func llamaLevelName(level int32) string {
	switch level {
	case 3:
		return "WARN"
	case 4:
		return "ERROR"
	default:
		return "LOG"
	}
}

// loadGGUF loads the llama.cpp library and GGUF model once per process and
// returns a pooling closure plus the model's embedding width. Context is
// checked between the load stages (the FFI calls themselves are not
// interruptible), so a cancelled request stops before the next one.
func loadGGUF(ctx context.Context, libPath, modelPath string) (fn func(ctx context.Context, text string) (Embedding, error), dims, ctxWindow, trainContext int, err error) {
	if modelPath == "" {
		return nil, 0, 0, 0, fmt.Errorf("gguf embed: model_path is required")
	}
	llamaOnce.Do(func() {
		// llama.Load binds logSetFunc, so LogSet can only run after it —
		// the library load itself must stay quiet by construction (yzma's
		// loader buffers its diagnostics into returned errors). Resolve
		// the empty-path case against the filesystem first so the loader's
		// default-search fallback can never enter the picture while the
		// default callback is still the loud one.
		if libPath == "" {
			libPath = os.Getenv("YZMA_LIB")
		}
		if libPath != "" {
			if _, err := os.Stat(libPath); err != nil {
				llamaLoadErr = fmt.Errorf("gguf embed: lib_path %s: %w", libPath, err)
				return
			}
		}
		if err := llama.Load(libPath); err != nil {
			llamaLoadErr = fmt.Errorf("gguf embed: load llama libraries from %s: %w", libPath, err)
			return
		}
		// Install the level-aware hook BEFORE Init(): the backend scan
		// ("load_backend: loaded CPU backend from …") prints through the
		// default log callback, which writes straight to this process's
		// terminal — inside the TUI that is a raw write onto tcell's
		// screen, and the interleaving with repaints garbles whatever
		// partial row the cursor sat on. The hook drops that noise but
		// keeps WARN and ERROR: the assert text of a GGML abort is the
		// only diagnosis a SIGABRT-in-decode leaves (yaah-x58j).
		llamaLogHook()
		if err := llama.Init(); err != nil {
			llamaLoadErr = fmt.Errorf("gguf embed: init backends: %w", err)
		}
	})
	if llamaLoadErr != nil {
		return nil, 0, 0, 0, llamaLoadErr
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, 0, 0, err
	}

	model, err := llama.ModelLoadFromFile(modelPath, llama.ModelDefaultParams())
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("gguf embed: load model %s: %w", modelPath, err)
	}
	if err := ctx.Err(); err != nil {
		llama.ModelFree(model)
		return nil, 0, 0, 0, err
	}
	threads := int32(runtime.NumCPU())
	if threads > 8 {
		threads = 8
	}
	// Decode window: never above the model's trained context. Decoding
	// more than n_ctx_train tokens aborts llama.cpp from inside the C
	// (SIGABRT during cgo — a corrupted terminal and a dead TUI; the
	// nomic model warns "n_ctx_seq > n_ctx_train -- possible training
	// context overflow" at 2048), so the window clamps to the model's own
	// train context. Longer inputs truncate at the closure below.
	ctxWindow = ggufNCtx
	trainContext = int(llama.ModelNCtxTrain(model))
	if trainContext > 0 && trainContext < ctxWindow {
		ctxWindow = trainContext
	}
	params := llama.ContextDefaultParams()
	params.NCtx = uint32(ctxWindow)
	params.NBatch = uint32(ctxWindow)
	// n_ubatch <= n_batch is llama.cpp's invariant; leaving NUbatch at the
	// 512 default would violate it for models training under 512.
	params.NUbatch = uint32(ctxWindow)
	params.Embeddings = 1
	params.NThreads = threads
	params.NThreadsBatch = threads
	// Pooling is the embedder's business (yaah-m3pk). Mean pooling matches
	// the embedding models yaah targets (nomic-embed, bge-m3, all-MiniLM).
	params.PoolingType = llama.PoolingTypeMean
	lctx, err := llama.InitFromModel(model, params)
	if err != nil {
		llama.ModelFree(model)
		return nil, 0, 0, 0, fmt.Errorf("gguf embed: init context: %w", err)
	}
	vocab := llama.ModelGetVocab(model)
	dims = int(llama.ModelNEmbd(model))
	if dims <= 0 {
		llama.Free(lctx)
		llama.ModelFree(model)
		return nil, 0, 0, 0, fmt.Errorf("gguf embed: model %s reports %d embedding dims", modelPath, dims)
	}

	fn = func(ctx context.Context, text string) (Embedding, error) {
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
		if len(tokens) > ctxWindow {
			tokens = tokens[:ctxWindow]
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
	return fn, dims, ctxWindow, trainContext, nil
}
