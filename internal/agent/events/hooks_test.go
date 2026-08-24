package events

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readHookFile(t *testing.T, dir, sessionID string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, sessionID+".jsonl"))
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	return string(data)
}

func TestHookEmitter_reusableAfterClose(t *testing.T) {
	dir := t.TempDir()
	h := NewHookEmitter(dir, "sess1")

	h.Emit(HookEvent{Event: SessionStart})
	h.Close()
	// A reused Loop emits again after the previous Run's teardown closed
	// the emitter; the write must land, not silently drop.
	h.Emit(HookEvent{Event: TurnStart})

	lines := strings.Split(strings.TrimSpace(readHookFile(t, dir, "sess1")), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 hook lines after close+re-emit, got %d: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], `"session.start"`) {
		t.Errorf("first line = %q; want session.start", lines[0])
	}
	if !strings.Contains(lines[1], `"turn.start"`) {
		t.Errorf("second line = %q; want turn.start", lines[1])
	}
	h.Close() // release the handle so TempDir cleanup works on Windows
}

func TestHookEmitter_sessionEndAfterClose(t *testing.T) {
	dir := t.TempDir()
	h := NewHookEmitter(dir, "sess2")

	h.Emit(HookEvent{Event: SessionStart})
	h.Close()
	// The teardown ordering contract: session.end emitted after Close
	// still lands because Emit re-opens the file.
	h.Emit(HookEvent{Event: SessionEnd, ExitReason: "completed"})

	content := readHookFile(t, dir, "sess2")
	if !strings.Contains(content, `"session.end"`) {
		t.Fatalf("session.end missing after Close; file content: %q", content)
	}
	h.Close() // release the handle so TempDir cleanup works on Windows
}

func TestHookEmitter_emptyDirIsNoop(t *testing.T) {
	h := NewHookEmitter("", "sess3")
	h.Emit(HookEvent{Event: SessionStart}) // must not panic or write
	h.Close()
}

func TestHookEmitter_failedOpenStickyNoRetry(t *testing.T) {
	// Block the hook directory path with a regular file so MkdirAll and
	// OpenFile fail. The emitter must swallow the error, stop retrying,
	// and never panic on later calls.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHookEmitter(filepath.Join(blocker, "sub"), "sess4")
	for i := 0; i < 3; i++ {
		h.Emit(HookEvent{Event: TurnStart})
	}
	h.Close()
}
