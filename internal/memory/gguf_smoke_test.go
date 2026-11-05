package memory

import (
	"bytes"
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
// the default log callback — a raw write to the process tty — and inside
// the TUI that interleave with tcell repaints into garbled screen
// artifacts. Run with YAAH_GGUF_SMOKE=1 (third review pass, yaah-x58j).
func TestGGUFSilentLoad(t *testing.T) {
	if os.Getenv("YAAH_GGUF_SMOKE") == "" {
		t.Skip("set YAAH_GGUF_SMOKE=1 to run")
	}

	capture := func() func() (string, string) {
		stdoutR, stdoutW, _ := os.Pipe()
		stderrR, stderrW, _ := os.Pipe()
		oldOut, oldErr := os.Stdout, os.Stderr
		os.Stdout, os.Stderr = stdoutW, stderrW
		restore := func() (string, string) {
			os.Stdout, os.Stderr = oldOut, oldErr
			stdoutW.Close()
			stderrW.Close()
			var outB, errB bytes.Buffer
			outB.ReadFrom(stdoutR)
			errB.ReadFrom(stderrR)
			stdoutR.Close()
			stderrR.Close()
			return outB.String(), errB.String()
		}
		return restore
	}

	restore := capture()
	e := NewGGUFEmbedder(os.Getenv("YAAH_GGUF_LIB"), os.Getenv("YAAH_GGUF_MODEL"), "search_query: ", "search_document: ")
	emb, err := e.EmbedQuery(context.Background(), "silent load guard")
	out, errOut := restore()
	if err != nil {
		t.Logf("captured stdout=%q stderr=%q", out, errOut)
		t.Fatalf("EmbedQuery: %v", err)
	}
	if len(emb) < 3 {
		t.Fatalf("embedding too short to validate: %d dims", len(emb))
	}
	if out != "" || errOut != "" {
		t.Errorf("first llama load leaked to the process tty: stdout=%q stderr=%q", out, errOut)
	}
}
