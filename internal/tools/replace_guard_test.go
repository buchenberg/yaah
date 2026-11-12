package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// yieldWorkspace delegates every call to the real workspace but yields the
// scheduler around each file operation, widening the window in which two
// unlocked replace calls interleave per file. Under the set lock it changes
// nothing: the second call is parked on the lock, not the scheduler.
type yieldWorkspace struct {
	Workspace
}

func (y *yieldWorkspace) ReadFile(ctx context.Context, path string) ([]byte, error) {
	yield()
	return y.Workspace.ReadFile(ctx, path)
}

func (y *yieldWorkspace) WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	yield()
	return y.Workspace.WriteFile(ctx, path, data, perm)
}

// TestReplace_ConcurrentRunsBothLand pins the multi-path serialization: two
// replace calls racing over the same directory must both land, because one
// set-lock acquisition orders each call's whole read → substitute → write
// pass. With a no-op set lock the two calls race per file and the loser's
// substitution is silently discarded (or refused as stale) — the exact bug
// this conversion fixes — which fails the final content assertion.
func TestReplace_ConcurrentRunsBothLand(t *testing.T) {
	ws, dir := realWorkspace(t)
	const files = 24
	for i := 0; i < files; i++ {
		writeFileAt(t, filepath.Join(dir, fmt.Sprintf("f%02d.txt", i)), "tokenAAA tokensep tokenBBB\n")
	}

	rt := &ReplaceTool{WS: &yieldWorkspace{Workspace: ws}}
	argsFor := func(pattern, replacement string) string {
		b, err := json.Marshal(map[string]any{
			"pattern":     pattern,
			"replacement": replacement,
			"path":        dir,
			"include":     "f*.txt",
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, sub := range [][2]string{{"tokenAAA", "G1OK"}, {"tokenBBB", "G2OK"}} {
		wg.Add(1)
		go func(i int, pattern, replacement string) {
			defer wg.Done()
			<-start
			_, errs[i] = rt.Execute(context.Background(), argsFor(pattern, replacement))
		}(i, sub[0], sub[1])
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent replace %d failed: %v", i, err)
		}
	}
	for i := 0; i < files; i++ {
		// Exact content, not just substring containment: a torn or
		// last-writer-wins file cannot satisfy this.
		assertFileContent(t, filepath.Join(dir, fmt.Sprintf("f%02d.txt", i)), "G1OK tokensep G2OK\n")
	}
}

// staleReadWorkspace delegates everything to a real local workspace but, on
// every ReadFile of one hooked path, returns the bytes that were on disk and
// then immediately rewrites the file out of band — a second writer (another
// process, an editor) replacing the file between the tool's read and its
// write. The hook is idempotent, so the second (verify) read observes the
// out-of-band bytes and the compare-and-swap fires.
type staleReadWorkspace struct {
	Workspace
	hookPath string
	oob      []byte
}

func (w *staleReadWorkspace) ReadFile(ctx context.Context, path string) ([]byte, error) {
	data, err := w.Workspace.ReadFile(ctx, path)
	if err == nil && path == w.hookPath {
		if werr := w.Workspace.WriteFile(ctx, path, w.oob, 0o644); werr != nil {
			return nil, werr
		}
	}
	return data, err
}

// TestReplace_RefusesStaleWrite pins the compare-and-swap inside a mutating
// run: the hijacked file is refused through an error carrying
// *tools.StaleWriteError (reachable via errors.As through the per-file
// error), the tool writes nothing to it, and the out-of-band bytes survive.
//
// Decision pinned here: a stale refusal does NOT abort the run — the other
// matched files are still written, matching how replace has always continued
// past per-file read and write failures.
func TestReplace_RefusesStaleWrite(t *testing.T) {
	ws, dir := realWorkspace(t)
	writeFileAt(t, filepath.Join(dir, "a.txt"), "keep AAA\n")
	hooked := filepath.Join(dir, "b.txt")
	writeFileAt(t, hooked, "keep AAA\n")
	writeFileAt(t, filepath.Join(dir, "c.txt"), "keep AAA\n")

	double := &staleReadWorkspace{Workspace: ws, hookPath: hooked, oob: []byte("out-of-band\n")}
	args, err := json.Marshal(map[string]any{
		"pattern":     "AAA",
		"replacement": "BBB",
		"path":        dir,
		"include":     "*.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&ReplaceTool{WS: double}).Execute(context.Background(), string(args))

	var stale *StaleWriteError
	if !errors.As(err, &stale) {
		t.Fatalf("replace error = %v (result %q), want an error carrying *StaleWriteError", err, result)
	}
	if stale.Path != "b.txt" {
		t.Errorf("stale path = %q, want b.txt", stale.Path)
	}
	if !strings.Contains(result, "file changed by another writer") {
		t.Errorf("report must carry the refusal line: %q", result)
	}

	// The refused file keeps the out-of-band bytes, untouched by the tool.
	assertFileContent(t, hooked, "out-of-band\n")
	// Pinned decision: the run continues past a stale refusal.
	assertFileContent(t, filepath.Join(dir, "a.txt"), "keep BBB\n")
	assertFileContent(t, filepath.Join(dir, "c.txt"), "keep BBB\n")
}

// TestReplace_GuardDryRunWritesNothing re-pins dry-run under the two-phase
// Execute: nothing is written, and the summary keeps its shape.
func TestReplace_GuardDryRunWritesNothing(t *testing.T) {
	ws, dir := realWorkspace(t)
	writeFileAt(t, filepath.Join(dir, "a.txt"), "hello\n")
	writeFileAt(t, filepath.Join(dir, "b.txt"), "hello world\n")

	args, err := json.Marshal(map[string]any{
		"pattern":     "hello",
		"replacement": "goodbye",
		"path":        dir,
		"include":     "*.txt",
		"dry_run":     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&ReplaceTool{WS: ws}).Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	if !strings.Contains(result, "DRY RUN — 2 file(s) with 2 match(es):") {
		t.Errorf("summary shape changed: %q", result)
	}
	if !strings.Contains(result, "a.txt: 1 match(es)") || !strings.Contains(result, "b.txt: 1 match(es)") {
		t.Errorf("per-file lines missing: %q", result)
	}
	assertFileContent(t, filepath.Join(dir, "a.txt"), "hello\n")
	assertFileContent(t, filepath.Join(dir, "b.txt"), "hello world\n")
}
