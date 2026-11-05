package memory

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hybridgroup/yzma/pkg/llama"
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
	if len(emb) < 3 {
		t.Fatalf("embedding too short to log: %d dims", len(emb))
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

// yaahSilentProbe marks the child process: an explicit argv flag the test
// binary itself registers, NOT an environment variable — a leaked ambient
// env value could else re-trigger the child branch in-process and
// reintroduce the vacuous pass this guard exists to eliminate.
var yaahSilentProbe = flag.Bool("yaah-silent-probe", false, "internal: run the gguf silent-load probe as the child process")

// TestGGUFSilentLoad guards the TUI-critical invariant: the FIRST llama
// load (library + backend scan + model + decode) must write nothing to
// stdout or stderr. The ggml backend loader's progress lines print through
// the default log callback — a raw fd-2 write — and inside the TUI that
// interleave with tcell repaints into garbled screen artifacts (yaah-x58j).
//
// The guard runs the whole load in a FRESH SUBPROCESS (the test binary
// re-invoking itself), because llamaOnce is process-global: in-process the
// earlier TestGGUFSmoke would consume the once-guard and the assertion
// would pass vacuously regardless of ordering. A child process also makes
// the capture fd-level: the child's raw fds 1/2 are the pipes the parent
// reads, so C-side writes (purego/llama.cpp) are caught too. Run with
// YAAH_GGUF_SMOKE=1.
func TestGGUFSilentLoad(t *testing.T) {
	if os.Getenv("YAAH_GGUF_SMOKE") == "" {
		t.Skip("set YAAH_GGUF_SMOKE=1 to run")
	}
	if *yaahSilentProbe {
		runSilentProbe()
		return
	}

	// Bounded: a hung FFI/backend call must surface as a probe failure,
	// not as a run until the outer go-test timeout. The deadline scales
	// for large models on slow disks via YAAH_GGUF_PROBE_TIMEOUT (a raw
	// duration; default 2m). A malformed override fails loudly: a silent
	// fallthrough would reproduce exactly the masked "signal: killed"
	// failure the override exists to prevent.
	deadline := 2 * time.Minute
	if v := os.Getenv("YAAH_GGUF_PROBE_TIMEOUT"); v != "" {
		d, perr := time.ParseDuration(v)
		switch {
		case perr != nil:
			t.Fatalf("YAAH_GGUF_PROBE_TIMEOUT=%q is not a duration: %v", v, perr)
		case d <= 0:
			t.Fatalf("YAAH_GGUF_PROBE_TIMEOUT=%q must be positive", v)
		}
		deadline = d
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run", "^TestGGUFSilentLoad$", "-yaah-silent-probe=true")
	cmd.Env = os.Environ() // YAAH_GGUF_SMOKE/LIB/MODEL flow through
	cmd.Stdin = nil
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("silent-load probe failed: %v\n%s", err, out)
	}
	// The child may print only the "OK dims=…" probe line and the test
	// binary's trailing "PASS". EVERYTHING else — including blank or
	// whitespace-only lines — is a leak that lands raw on the tty.
	for _, ln := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if ln == "PASS" || strings.HasPrefix(ln, "OK dims=") {
			continue
		}
		t.Errorf("first llama load leaked to the process tty — unexpected child output line %q (full output:\n%s)", ln, out)
		break
	}
}

// runSilentProbe is the subprocess body: one full embed with the model's
// own stdout summary and nothing else.
func runSilentProbe() {
	e := NewGGUFEmbedder(os.Getenv("YAAH_GGUF_LIB"), os.Getenv("YAAH_GGUF_MODEL"), "search_query: ", "search_document: ")
	childFailed := func(err error) {
		// Errors go to Go's log (stderr is ours); exit nonzero so the
		// parent's CombinedOutput non-empty-fails it anyway.
		fmt.Fprintln(os.Stderr, "probe error:", err)
		os.Exit(1)
	}
	emb, err := e.EmbedQuery(context.Background(), "silent load guard")
	if err != nil {
		childFailed(err)
	}
	if len(emb) < 3 {
		childFailed(fmt.Errorf("embedding too short to validate: %d dims", len(emb)))
	}
	fmt.Printf("OK dims=%d first=[%.6g]\n", len(emb), emb[0])
}

// TestGGUF_ClampDecodWindowToTrainContext pins the SIGABRT fix
// (yaah-x58j): llama.cpp aborts inside cgo when decode runs more tokens
// than the model's n_ctx_train (the nomic-v2-moe GGUF trains at 512 while
// the embedder reserved 2048 — the crash took the whole TUI down with a
// raw runtime dump onto the alt screen). The decode window must clamp to
// the model's own train context, so an input many times over it embeds
// without aborting. Run with YAAH_GGUF_SMOKE=1.
func TestGGUF_ClampDecodeWindowToTrainContext(t *testing.T) {
	if os.Getenv("YAAH_GGUF_SMOKE") == "" || os.Getenv("YAAH_GGUF_MODEL") == "" {
		t.Skip("set YAAH_GGUF_SMOKE=1 with YAAH_GGUF_LIB/MODEL to run")
	}
	lib, model := os.Getenv("YAAH_GGUF_LIB"), os.Getenv("YAAH_GGUF_MODEL")
	e := NewGGUFEmbedder(lib, model, "search_query: ", "search_document: ")
	// The probe forces the lazy load; its vector is unused.
	if _, perr := e.Embed(context.Background(), "clamp probe"); perr != nil {
		t.Fatalf("probe embed: %v", perr)
	}

	// Assert the clamp actually took: the window must be the model's own
	// train context whenever that is below the reserved default. Without
	// the clamp the window stays 2048 regardless of the train context.
	window := e.DecodeWindow()
	if window <= 0 || window > ggufNCtx {
		t.Fatalf("decode window %d out of range (reserved %d)", window, ggufNCtx)
	}
	llama.Load(lib) // process-global; already loaded, binds no new state
	m, err := llama.ModelLoadFromFile(model, llama.ModelDefaultParams())
	if err != nil {
		t.Fatalf("model load (for n_ctx_train): %v", err)
	}
	defer llama.ModelFree(m)
	train := int(llama.ModelNCtxTrain(m))
	if train > 0 && train < ggufNCtx && window != train {
		t.Fatalf("decode window %d is not clamped to the model's train context %d", window, train)
	}

	// Size the fixture past the actual window: ~1 token per 3.5 chars of
	// prose, so window*7 chars decodes at >= 2x the window in tokens — the
	// shape of the message whose embed aborted the live TUI.
	long := strings.Repeat("The repository layout maps workspace grants to dispatch worlds, and the settle path serializes per parent. ", window*7/len("The repository layout maps workspace grants to dispatch worlds, and the settle path serializes per parent. ")+1)
	if got := llama.Tokenize(llama.ModelGetVocab(m), long, true, true); int32(len(got)) <= int32(window) {
		t.Fatalf("fixture under-exercises the clamp: %d tokens <= window %d", len(got), window)
	}
	emb, err := e.Embed(context.Background(), long)
	if err != nil {
		t.Fatalf("over-window embed must not abort: %v", err)
	}
	if len(emb) != e.Dims() || len(emb) == 0 {
		t.Fatalf("unexpected embedding: %d dims (model %d)", len(emb), e.Dims())
	}
}
