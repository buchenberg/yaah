package memory

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
// reads, so C-side writes (purego/llama.cpp) are caught too (third review
// pass kilo-code-bot). Run with YAAH_GGUF_SMOKE=1.
func TestGGUFSilentLoad(t *testing.T) {
	if os.Getenv("YAAH_GGUF_SMOKE") == "" {
		t.Skip("set YAAH_GGUF_SMOKE=1 to run")
	}
	if os.Getenv("YAAH_GGUF_SILENT_CHILD") == "1" {
		runSilentProbe()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run", "^TestGGUFSilentLoad$", "-test.v=false")
	cmd.Env = append(os.Environ(), "YAAH_GGUF_SILENT_CHILD=1")
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("silent-load probe failed: %v\n%s", err, out)
	}
	// The child may print only its own probe sentinel lines and the test
	// binary's trailing "PASS". Every other byte is a leak.
	allowed := 0
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if ln == "" || ln == "PASS" || strings.HasPrefix(ln, "OK dims=") {
			allowed++
			continue
		}
	}
	if allowed != len(strings.Split(strings.TrimSpace(string(out)), "\n")) {
		t.Errorf("first llama load leaked to the process tty — child output:\n%s", out)
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
