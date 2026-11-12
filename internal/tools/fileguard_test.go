package tools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeFileInfo is the minimal fs.FileInfo the in-memory double hands back.
type fakeFileInfo struct {
	name string
	size int64
	mode fs.FileMode
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any           { return nil }

// guardWorkspace is an in-memory Workspace double for the lock tests: no
// disk, no scheduler assumptions. The embedded interface is nil on purpose —
// withFileWrite calls only the methods overridden below, and anything else
// panics loudly instead of silently reaching the real filesystem.
type guardWorkspace struct {
	Workspace

	root string

	mu    sync.Mutex
	files map[string][]byte
}

func newGuardWorkspace(root string) *guardWorkspace {
	return &guardWorkspace{root: root, files: make(map[string][]byte)}
}

func (g *guardWorkspace) ResolvePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	return filepath.Join(g.root, path), nil
}

func (g *guardWorkspace) WorkDir() string { return g.root }

func (g *guardWorkspace) Lstat(_ context.Context, path string) (fs.FileInfo, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	data, ok := g.files[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return fakeFileInfo{name: filepath.Base(path), size: int64(len(data)), mode: 0o644}, nil
}

func (g *guardWorkspace) ReadFile(_ context.Context, path string) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	data, ok := g.files[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}

func (g *guardWorkspace) WriteFile(_ context.Context, path string, data []byte, _ fs.FileMode) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.files[path] = append([]byte(nil), data...)
	return nil
}

// realWorkspace builds the established test construction: a local workspace
// whose validator roots, resolves, and displays paths under a temp dir.
func realWorkspace(t *testing.T) (Workspace, string) {
	t.Helper()
	dir := t.TempDir()
	pv := NewPathValidator(dir, false, nil)
	pv.WorkDir = pv.WorkspaceRoot
	return NewLocalWorkspace(pv), dir
}

// yield lets a spawned goroutine make progress without sleeping: the probe
// that follows is a non-blocking check, so scheduling cannot flip the verdict.
func yield() {
	for i := 0; i < 100; i++ {
		runtime.Gosched()
	}
}

// TestWithFileWrite_SerializesHoldersPerPath pins the per-path critical
// section: while one holder is parked inside mutate, a second withFileWrite on
// the same path must not enter, and must enter promptly once the first
// releases. All handshakes are channels; the goroutine yields replace sleeps.
func TestWithFileWrite_SerializesHoldersPerPath(t *testing.T) {
	ws := newGuardWorkspace(t.TempDir())
	abs := filepath.Join(ws.root, "guarded.txt")

	enteredFirst := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = withFileWrite(context.Background(), ws, abs, func([]byte, fs.FileMode) ([]byte, error) {
			close(enteredFirst)
			<-releaseFirst // hold the per-path critical section open
			return []byte("first"), nil
		})
	}()
	<-enteredFirst // the first holder is inside mutate: the lock is held

	enteredSecond := make(chan struct{})
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		_, _ = withFileWrite(context.Background(), ws, abs, func([]byte, fs.FileMode) ([]byte, error) {
			close(enteredSecond)
			return []byte("second"), nil
		})
	}()

	// The second writer must be parked on the per-path lock. A healthy lock
	// can never let enteredSecond fire here no matter how the scheduler runs;
	// a no-op lock lets the goroutine run and fails the probe.
	yield()
	select {
	case <-enteredSecond:
		t.Fatal("second writer entered withFileWrite while the first held the per-path lock")
	default:
	}

	close(releaseFirst)
	<-firstDone
	<-enteredSecond
	<-secondDone
}

// TestWithFileWrite_RefusesStaleWrite pins the compare-and-swap: a writer
// that replaces the file between the guard's read and its write gets a
// *StaleWriteError carrying both full digests, and the file keeps the other
// writer's bytes.
func TestWithFileWrite_RefusesStaleWrite(t *testing.T) {
	ws, dir := realWorkspace(t)
	abs := filepath.Join(dir, "stale.txt")
	original := []byte("original bytes")
	if err := os.WriteFile(abs, original, 0o600); err != nil {
		t.Fatal(err)
	}

	receipt, err := withFileWrite(context.Background(), ws, abs, func(cur []byte, mode fs.FileMode) ([]byte, error) {
		if string(cur) != string(original) {
			t.Errorf("cur = %q, want the on-disk content %q", cur, original)
		}
		if mode.Perm() != 0o600 {
			t.Errorf("mode = %o, want the existing file's 0600", mode.Perm())
		}
		// An out-of-band writer (another process, an editor) replaces the
		// file while the tool computes its result.
		if werr := os.WriteFile(abs, []byte("out-of-band"), 0o600); werr != nil {
			return nil, werr
		}
		return []byte("tool result"), nil
	})

	var stale *StaleWriteError
	if !errors.As(err, &stale) {
		t.Fatalf("withFileWrite error = %v, want *StaleWriteError", err)
	}
	if receipt != (FileWriteReceipt{}) {
		t.Errorf("receipt = %+v, want the zero receipt on a refused write", receipt)
	}
	wantExpected := proposalContentDigest(original)
	wantFound := proposalContentDigest([]byte("out-of-band"))
	if stale.Expected != wantExpected || stale.Found != wantFound {
		t.Errorf("digests = (%s, %s), want (%s, %s)", stale.Expected, stale.Found, wantExpected, wantFound)
	}
	if len(stale.Expected) != 64 || len(stale.Found) != 64 {
		t.Errorf("digests must be full 64-char hex, got %d and %d chars", len(stale.Expected), len(stale.Found))
	}
	msg := stale.Error()
	if !strings.HasPrefix(msg, "stale.txt: file changed by another writer ") {
		t.Errorf("message must start with the path and the reason: %q", msg)
	}
	if !strings.Contains(msg, wantExpected) || !strings.Contains(msg, wantFound) {
		t.Errorf("message must carry both digests: %q", msg)
	}

	got, rerr := os.ReadFile(abs)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != "out-of-band" {
		t.Errorf("file content = %q, want the out-of-band bytes untouched", got)
	}
}

// TestWithFileWrite_ReceiptExistingFile pins the receipt and its String()
// rendering for a file that already exists: byte delta, no Created flag, the
// post-write digest, and the existing mode handed to mutate.
func TestWithFileWrite_ReceiptExistingFile(t *testing.T) {
	ws, dir := realWorkspace(t)
	abs := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(abs, []byte("abc"), 0o640); err != nil {
		t.Fatal(err)
	}

	var gotMode fs.FileMode
	receipt, err := withFileWrite(context.Background(), ws, abs, func(cur []byte, mode fs.FileMode) ([]byte, error) {
		gotMode = mode
		if string(cur) != "abc" {
			t.Errorf("cur = %q, want the file's current content", cur)
		}
		return []byte("abcdef"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMode.Perm() != 0o640 {
		t.Errorf("mutate mode = %o, want the existing file's 0640", gotMode.Perm())
	}

	want := FileWriteReceipt{
		Path:        "existing.txt",
		BeforeBytes: 3,
		AfterBytes:  6,
		Digest:      proposalContentDigest([]byte("abcdef")),
	}
	if receipt != want {
		t.Errorf("receipt = %+v, want %+v", receipt, want)
	}
	wantLine := fmt.Sprintf("wrote existing.txt (3 -> 6 bytes, sha256:%s)", want.Digest[:12])
	if receipt.String() != wantLine {
		t.Errorf("String() = %q, want %q", receipt.String(), wantLine)
	}

	got, rerr := os.ReadFile(abs)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != "abcdef" {
		t.Errorf("file content = %q, want the written bytes", got)
	}
	info, serr := os.Stat(abs)
	if serr != nil {
		t.Fatal(serr)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("file mode = %o, want the preserved 0640", info.Mode().Perm())
	}
}

// TestWithFileWrite_ReceiptNewFile pins the missing-file path: nil cur, mode
// 0o644 into mutate, Created with BeforeBytes -1, the default write mode, and
// the "created" String() rendering.
func TestWithFileWrite_ReceiptNewFile(t *testing.T) {
	ws, dir := realWorkspace(t)
	abs := filepath.Join(dir, "new.txt")

	var gotCur []byte = []byte("sentinel")
	var gotMode fs.FileMode
	receipt, err := withFileWrite(context.Background(), ws, abs, func(cur []byte, mode fs.FileMode) ([]byte, error) {
		gotCur, gotMode = cur, mode
		return []byte("0123456789"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotCur != nil {
		t.Errorf("cur = %q, want nil for a missing file", gotCur)
	}
	if gotMode != 0o644 {
		t.Errorf("mode = %o, want 0644 for a missing file", gotMode)
	}

	want := FileWriteReceipt{
		Path:        "new.txt",
		BeforeBytes: -1,
		AfterBytes:  10,
		Digest:      proposalContentDigest([]byte("0123456789")),
		Created:     true,
	}
	if receipt != want {
		t.Errorf("receipt = %+v, want %+v", receipt, want)
	}
	wantLine := fmt.Sprintf("created new.txt (0 -> 10 bytes, sha256:%s)", want.Digest[:12])
	if receipt.String() != wantLine {
		t.Errorf("String() = %q, want %q", receipt.String(), wantLine)
	}

	got, rerr := os.ReadFile(abs)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != "0123456789" {
		t.Errorf("file content = %q, want the written bytes", got)
	}
	info, serr := os.Stat(abs)
	if serr != nil {
		t.Fatal(serr)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("file mode = %o, want the default 0644", info.Mode().Perm())
	}
}

// TestWithFileWriteSet_HoldsEveryPathThenReleases pins the multi-path shape:
// while withFileWriteSet's fn runs, neither named path can be entered, and
// both become enterable the moment it returns. The final waits are bounded so
// a leaked lock fails the test instead of hanging it.
func TestWithFileWriteSet_HoldsEveryPathThenReleases(t *testing.T) {
	ws := newGuardWorkspace(t.TempDir())
	a := filepath.Join(ws.root, "a.txt")
	b := filepath.Join(ws.root, "b.txt")

	type writer struct {
		entered chan struct{}
		done    chan struct{}
	}
	startWriter := func(path string) writer {
		w := writer{entered: make(chan struct{}), done: make(chan struct{})}
		go func() {
			defer close(w.done)
			_, _ = withFileWrite(context.Background(), ws, path, func([]byte, fs.FileMode) ([]byte, error) {
				close(w.entered)
				return []byte("w"), nil
			})
		}()
		return w
	}
	bounded := func(ch <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s never happened; a lock leaked from withFileWriteSet", what)
		}
	}

	var wa, wb writer
	err := withFileWriteSet([]string{a, b}, func() error {
		wa = startWriter(a)
		wb = startWriter(b)

		yield()
		select {
		case <-wa.entered:
			t.Fatal("writer entered path a while withFileWriteSet held it")
		default:
		}
		select {
		case <-wb.entered:
			t.Fatal("writer entered path b while withFileWriteSet held it")
		default:
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withFileWriteSet error = %v, want nil", err)
	}
	if wa.entered == nil || wb.entered == nil {
		t.Fatal("fn never ran its writers")
	}

	// Released: every path must now be enterable, and the writers finish.
	bounded(wa.entered, "writer a entered after release")
	bounded(wb.entered, "writer b entered after release")
	bounded(wa.done, "writer a finished")
	bounded(wb.done, "writer b finished")
}

// TestWithFileWriteSet_ReturnsFnError pins that fn's error travels out
// unchanged.
func TestWithFileWriteSet_ReturnsFnError(t *testing.T) {
	sentinel := errors.New("boom")
	if err := withFileWriteSet(nil, func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want the fn error unchanged", err)
	}
}

// TestGuardRoutedToolsImplementSelfGuardsPath pins the invariant that makes
// the RecordingTool wrapper safe: every tool that takes the per-path lock
// inside its own Execute must implement selfGuardingTool, or the wrapper
// acquires the same non-reentrant lock first and the tool deadlocks on
// itself. It was missing on PatchTool once; nothing else noticed, because a
// deadlock needs a sub-agent dispatch to show up. A new guard-routed tool
// belongs in this list.
func TestGuardRoutedToolsImplementSelfGuardsPath(t *testing.T) {
	guardRouted := []struct {
		name string
		tool Tool
	}{
		{"append", &AppendTool{}},
		{"edit", &EditTool{}},
		{"json_query", &JSONQueryTool{}},
		{"patch", &PatchTool{}},
		{"replace", &ReplaceTool{}},
		{"write", &WriteTool{}},
	}
	for _, tt := range guardRouted {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := tt.tool.(selfGuardingTool); !ok {
				t.Fatalf("%s takes the per-path write guard inside Execute but does not implement selfGuardsPath: the RecordingTool wrapper would hold the same non-reentrant lock across it and deadlock", tt.name)
			}
		})
	}
}
