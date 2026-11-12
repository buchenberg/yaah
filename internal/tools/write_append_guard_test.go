package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests pin the write/append conversion onto the per-path file guard
// (fileguard.go) at the TOOL boundary: fileguard_test.go pins the lock and
// the compare-and-swap primitives; these pin that the tools route through
// them and that the ledger contract (append) and the replacement contract
// (write) hold under concurrency.

// TestAppendGuard_ConcurrentAppendsAllLand is the lost-update regression:
// eight dispatches appending distinct JSON records to one ledger file at the
// same moment must all land, each intact — no torn records, no losses.
func TestAppendGuard_ConcurrentAppendsAllLand(t *testing.T) {
	ws, dir := realWorkspace(t)
	path := filepath.Join(dir, "ledger.jsonl")

	const workers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			tool := &AppendTool{WS: ws}
			line := fmt.Sprintf(`{"worker":%d}`+"\n", w)
			args := fmt.Sprintf(`{"filePath":%q,"content":%q}`, path, line)
			<-start
			_, errs[w] = tool.Execute(context.Background(), args)
		}(w)
	}
	close(start)
	wg.Wait()

	for w, err := range errs {
		if err != nil {
			t.Fatalf("worker %d append: %v", w, err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != workers {
		t.Fatalf("records = %d (%q), want %d intact records", len(lines), data, workers)
	}
	seen := make(map[string]bool, workers)
	for _, line := range lines {
		var rec struct {
			Worker int `json:"worker"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil || seen[line] {
			t.Fatalf("torn or duplicated record %q in %q", line, data)
		}
		seen[line] = true
	}
	if len(seen) != workers {
		t.Fatalf("distinct records = %d, want %d", len(seen), workers)
	}
}

// TestAppendGuard_RewriteVsAppendNeverTears pins the serialization between a
// full rewrite (write, under withFileWrite) and appends: every state an
// observer can sample is the rewrite's content plus zero or more complete
// appended records — never a fragment — and no write is ever refused as
// stale, because the per-path lock keeps an append out of the writer's
// compare-and-swap window.
func TestAppendGuard_RewriteVsAppendNeverTears(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows has mandatory file locking: an atomic write is a rename over
		// the live file, which the OS refuses while any other handle (here the
		// observing reader, and an in-flight append) has it open — "Access is
		// denied", not a torn record. The interleaving this test pins is a
		// POSIX property; the serialization it relies on is covered on every
		// platform by TestWithFileWrite_SerializesHoldersPerPath and
		// TestAppendGuard_ConcurrentAppendsAllLand.
		t.Skip("atomic replace over an open file is refused by Windows, not torn")
	}
	ws, dir := realWorkspace(t)
	path := filepath.Join(dir, "ledger.jsonl")
	const rewrite = "REWRITE\n"
	const rounds = 60

	// Baseline: the file already holds a rewrite result before the race
	// starts, so every legal sample starts with the rewrite prefix.
	wt := &WriteTool{WS: ws}
	if _, err := wt.Execute(context.Background(), fmt.Sprintf(`{"filePath":%q,"content":%q}`, path, rewrite)); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	writeErrs := make(chan error, rounds)
	appendErrs := make(chan error, rounds)

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < rounds; i++ {
			_, err := wt.Execute(context.Background(), fmt.Sprintf(`{"filePath":%q,"content":%q}`, path, rewrite))
			writeErrs <- err
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		at := &AppendTool{WS: ws}
		<-start
		for i := 0; i < rounds; i++ {
			line := fmt.Sprintf(`{"line":%d}`+"\n", i)
			_, err := at.Execute(context.Background(), fmt.Sprintf(`{"filePath":%q,"content":%q}`, path, line))
			appendErrs <- err
		}
	}()

	close(start)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	// Observer: every sample must be the rewrite content followed only by
	// complete appended records.
	samples := 0
	for running := true; running; {
		select {
		case <-done:
			running = false
		default:
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("observer read: %v", err)
		}
		if !strings.HasPrefix(string(data), rewrite) {
			t.Fatalf("sample without the rewrite prefix (torn base): %q", data)
		}
		for rem := string(data)[len(rewrite):]; len(rem) > 0; {
			nl := strings.IndexByte(rem, '\n')
			if nl < 0 {
				t.Fatalf("partial appended record in sample %d: %q", samples, data)
			}
			line := rem[:nl]
			rem = rem[nl+1:]
			var rec struct {
				Line int `json:"line"`
			}
			if !strings.HasPrefix(line, `{"line":`) || json.Unmarshal([]byte(line), &rec) != nil {
				t.Fatalf("interleaved or torn record %q in sample %q", line, data)
			}
		}
		samples++
	}
	if samples == 0 {
		t.Fatal("observer never sampled the file")
	}

	// Every operation must have succeeded. A *StaleWriteError here means an
	// append landed inside a writer's compare-and-snap window: the per-path
	// lock failed to serialize rewrite against append.
	for i := 0; i < rounds; i++ {
		if err := <-writeErrs; err != nil {
			var stale *StaleWriteError
			if errors.As(err, &stale) {
				t.Fatalf("write %d refused as stale: an append interleaved with the writer's snapshot window: %v", i, err)
			}
			t.Fatalf("write %d: %v", i, err)
		}
		if err := <-appendErrs; err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
}

// staleRewriteWorkspace is a Workspace double over the real local workspace:
// reading the target path returns the real bytes and then immediately
// rewrites the file out of band — the shape of an external writer (another
// process, an editor) landing between the guard's read and its write.
type staleRewriteWorkspace struct {
	Workspace
	target string
}

func (w *staleRewriteWorkspace) ReadFile(ctx context.Context, path string) ([]byte, error) {
	data, err := w.Workspace.ReadFile(ctx, path)
	if err == nil && resolvedPath(path) == resolvedPath(w.target) {
		if werr := os.WriteFile(path, []byte("out-of-band"), 0o600); werr != nil {
			return nil, werr
		}
	}
	return data, err
}

// TestWriteGuard_RefusesStaleWrite pins the compare-and-swap at the tool
// boundary: when the file changes under the tool, write is refused with a
// *StaleWriteError reachable through the tool's own error wrap, and the
// out-of-band bytes survive untouched.
func TestWriteGuard_RefusesStaleWrite(t *testing.T) {
	ws, dir := realWorkspace(t)
	path := filepath.Join(dir, "stale.txt")
	if err := os.WriteFile(path, []byte("original bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	tool := &WriteTool{WS: &staleRewriteWorkspace{Workspace: ws, target: path}}
	_, err := tool.Execute(context.Background(), fmt.Sprintf(`{"filePath":%q,"content":%q}`, path, "tool result"))
	if err == nil {
		t.Fatal("write over an out-of-band change must fail")
	}
	var stale *StaleWriteError
	if !errors.As(err, &stale) {
		t.Fatalf("error = %v, want *StaleWriteError through the write wrap", err)
	}
	if msg := err.Error(); !strings.HasPrefix(msg, "write: ") || !strings.Contains(msg, "file changed by another writer") {
		t.Errorf("message = %q, want the write wrap around the stale refusal", msg)
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(data) != "out-of-band" {
		t.Errorf("content = %q, want the out-of-band bytes untouched", data)
	}
}

// TestWriteGuard_CreatesNewFileAndNotifiesOverwrite pins what the guard must
// not change: a missing path is created 0o644, an overwrite keeps the
// existing mode, the success message keeps its format, and the advisory
// overwrite notice (file_existed) still fires — now sourced from the guard's
// snapshot (cur == nil) instead of a separate stat.
func TestWriteGuard_CreatesNewFileAndNotifiesOverwrite(t *testing.T) {
	ws, dir := realWorkspace(t)
	tool := &WriteTool{WS: ws}

	fresh := filepath.Join(dir, "fresh.txt")
	msg, err := tool.Execute(context.Background(), fmt.Sprintf(`{"filePath":%q,"content":%q}`, fresh, "first"))
	if err != nil {
		t.Fatal(err)
	}
	// The success line is unchanged; the guard's receipt line follows it.
	first, _, _ := strings.Cut(msg, "\n")
	prefix := fmt.Sprintf("Wrote %d bytes to ", len("first"))
	if !strings.HasPrefix(first, prefix) || resolvedPath(strings.TrimPrefix(first, prefix)) != resolvedPath(fresh) {
		t.Errorf("message = %q, want the unchanged success format on the first line", msg)
	}
	info, serr := os.Stat(fresh)
	if serr != nil {
		t.Fatal(serr)
	}
	wantFileMode(t, info.Mode(), 0o644, "fresh mode")
	data, rerr := os.ReadFile(fresh)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(data) != "first" {
		t.Errorf("fresh content = %q, want %q", data, "first")
	}

	existing := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(existing, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any(nil)
	ctx := WithSupervisionMeta(context.Background(), &meta)
	if _, err := tool.Execute(ctx, fmt.Sprintf(`{"filePath":%q,"content":%q}`, existing, "new")); err != nil {
		t.Fatal(err)
	}
	if meta["file_existed"] != true {
		t.Errorf("file_existed = %v, want the overwrite notice", meta["file_existed"])
	}
	info, serr = os.Stat(existing)
	if serr != nil {
		t.Fatal(serr)
	}
	wantFileMode(t, info.Mode(), 0o600, "overwrite mode")
	data, rerr = os.ReadFile(existing)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(data) != "new" {
		t.Errorf("overwritten content = %q, want %q", data, "new")
	}

	noticePath := filepath.Join(dir, "notice.txt")
	meta = map[string]any(nil)
	ctx = WithSupervisionMeta(context.Background(), &meta)
	if _, err := tool.Execute(ctx, fmt.Sprintf(`{"filePath":%q,"content":%q}`, noticePath, "x")); err != nil {
		t.Fatal(err)
	}
	if meta["file_existed"] != false {
		t.Errorf("file_existed = %v, want false for first-time creation", meta["file_existed"])
	}
}

// TestRecordingTool_GuardedWriteCompletes pins the wrapper interplay: a
// guard-routed write tool under RecordingTool with a tracked dispatch must
// complete. The wrapper must not hold the per-path lock across a tool that
// takes it itself — the lock is not reentrant per goroutine — and the
// deadline turns a regression here into a failure instead of a hung suite.
func TestRecordingTool_GuardedWriteCompletes(t *testing.T) {
	ws, dir := realWorkspace(t)
	rt := NewRecordingTool(&WriteTool{WS: ws}, &ConflictTracker{})
	rt.SetWorkspace(ws)
	ctx := WithConflictDispatch(context.Background(), "worker", ConflictLaneShared)
	args := fmt.Sprintf(`{"filePath":%q,"content":%q}`, filepath.Join(dir, "f.txt"), "x")

	done := make(chan error, 1)
	go func() {
		_, err := rt.Execute(ctx, args)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RecordingTool deadlocked around the guard-routed write tool")
	}
}
