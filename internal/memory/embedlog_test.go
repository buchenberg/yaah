package memory

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a goroutine-safe bytes.Buffer so async embed failures
// can log into it while the test asserts under -race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureWarnLogs redirects the slog default logger into a buffer at
// warn level for the duration of the test and returns the buffer.
func captureWarnLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

func TestEmbedWarnLimiter_FirstWarningEmitted(t *testing.T) {
	logs := captureWarnLogs(t)
	var l embedWarnLimiter

	if !l.warn("embed_memory", errors.New("embed server down")) {
		t.Fatal("first warning must be emitted")
	}
	out := logs.String()
	if !strings.Contains(out, "embedding failed") {
		t.Errorf("log missing message: %q", out)
	}
	if !strings.Contains(out, "embed_memory") {
		t.Errorf("log missing op name: %q", out)
	}
	if !strings.Contains(out, "embed server down") {
		t.Errorf("log missing error: %q", out)
	}
}

func TestEmbedWarnLimiter_SuppressesWithinInterval(t *testing.T) {
	logs := captureWarnLogs(t)
	now := time.Unix(1_700_000_000, 0)
	l := embedWarnLimiter{interval: 30 * time.Second, nowFn: func() time.Time { return now }}
	err := errors.New("boom")

	if !l.warn("search_memory_vector", err) {
		t.Fatal("first warning must be emitted")
	}
	first := logs.String()

	now = now.Add(29 * time.Second)
	if l.warn("search_memory_vector", err) {
		t.Error("warning within the quiet window must be suppressed")
	}
	if l.warn("search_memory_vector", err) {
		t.Error("warning within the quiet window must be suppressed")
	}
	if logs.String() != first {
		t.Error("suppressed warnings must not write to the log")
	}

	now = now.Add(2 * time.Second) // 31s after the first warning
	if !l.warn("search_memory_vector", err) {
		t.Fatal("warning after the quiet window must be emitted")
	}
	out := logs.String()
	if !strings.Contains(out, "suppressed=2") {
		t.Errorf("emitted warning must carry the suppressed count: %q", out)
	}
}

func TestEmbedWarnLimiter_ZeroValueUsable(t *testing.T) {
	logs := captureWarnLogs(t)
	var l embedWarnLimiter

	if !l.warn("embed_message", errors.New("x")) {
		t.Error("zero value must emit the first warning")
	}
	if l.warn("embed_message", errors.New("x")) {
		t.Error("zero value must rate-limit at the default interval")
	}
	if logs.String() == "" {
		t.Error("zero value produced no log output")
	}
}

func TestEmbedWarnLimiter_Concurrent(t *testing.T) {
	captureWarnLogs(t)
	var l embedWarnLimiter
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.warn("reconcile_embeddings", errors.New("x"))
		}()
	}
	wg.Wait()

	l.mu.Lock()
	defer l.mu.Unlock()
	// Exactly one warning passes the limiter; the rest are counted.
	if got := 1 + l.suppressed; got != 50 {
		t.Errorf("emitted+suppressed = %d, want 50", got)
	}
}

func TestDB_RecordEmbedFailure_NoMetricsNoPanic(t *testing.T) {
	logs := captureWarnLogs(t)
	db := openTestDB(t)

	// Metrics are not initialised in unit tests — the recorder must be a
	// guarded no-op and the warning must still surface.
	db.recordEmbedFailure("embed_memory", errors.New("server down"))

	out := logs.String()
	if !strings.Contains(out, "embedding failed") || !strings.Contains(out, "embed_memory") {
		t.Errorf("warning not surfaced: %q", out)
	}
}
