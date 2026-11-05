package yaah

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/config"
)

// probeLogBuf is a goroutine-safe sink for the startup probe's warnings.
type probeLogBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *probeLogBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *probeLogBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureProbeLogs redirects the default logger at warn level for the test,
// so a Debug line (reachable embedder) cannot be mistaken for a warning.
func captureProbeLogs(t *testing.T) *probeLogBuf {
	t.Helper()
	buf := &probeLogBuf{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

// probeCfg builds a config whose embedding provider points at baseURL.
func probeCfg(baseURL string) *config.Config {
	return &config.Config{
		Providers: map[string]config.Provider{
			"embed": {BaseURL: baseURL, Name: "Embed"},
		},
		Embedding: config.EmbeddingConfig{Provider: "embed", Model: "nomic-embed-text"},
	}
}

// TestCheckEmbedderAtStartup_UnreachableWarnsAndProceeds is the yaah-ka6
// regression: a dead embed server must produce ONE clear warning and must not
// fail anything.
func TestCheckEmbedderAtStartup_UnreachableWarnsAndProceeds(t *testing.T) {
	logs := captureProbeLogs(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening: connection refused

	if reachable := probeAttached(probeCfg(url)); reachable {
		t.Error("checkEmbedderAtStartup reported an unreachable embedder as reachable")
	}

	out := logs.String()
	if !strings.Contains(out, "unreachable") {
		t.Errorf("no unreachable warning emitted: %q", out)
	}
	if !strings.Contains(out, url) {
		t.Errorf("warning must name the address: %q", out)
	}
	if !strings.Contains(out, "keyword") {
		t.Errorf("warning must say what degrades (keyword fallback): %q", out)
	}
	// One probe, one warning — the caller probes once per session and the
	// function must not add duplicates of its own.
	if n := strings.Count(out, "unreachable"); n != 1 {
		t.Errorf("warning emitted %d times, want exactly 1: %q", n, out)
	}
}

// TestCheckEmbedderAtStartup_ReachableSilent covers the healthy path: a probe
// that answers must not warn.
func TestCheckEmbedderAtStartup_ReachableSilent(t *testing.T) {
	logs := captureProbeLogs(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float64{0.1, 0.2, -0.3}}},
		})
	}))
	defer srv.Close()

	if reachable := probeAttached(probeCfg(srv.URL)); !reachable {
		t.Error("checkEmbedderAtStartup reported a reachable embedder as unreachable")
	}
	if out := logs.String(); out != "" {
		t.Errorf("reachable embedder must not warn: %q", out)
	}
}

// TestCheckEmbedderAtStartup_NoEmbedderConfigured covers a session with no
// embedding stanza: nothing to probe, nothing to say.
func TestCheckEmbedderAtStartup_NoEmbedderConfigured(t *testing.T) {
	logs := captureProbeLogs(t)

	cases := map[string]*config.Config{
		"empty config":    {},
		"no provider set": {Embedding: config.EmbeddingConfig{Model: "nomic-embed-text"}},
		"no model set": {Providers: map[string]config.Provider{"embed": {BaseURL: "http://127.0.0.1:1"}},
			Embedding: config.EmbeddingConfig{Provider: "embed"}},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if reachable := probeAttached(cfg); reachable {
				t.Error("checkEmbedderAtStartup reported reachable with no embedder configured")
			}
		})
	}
	if out := logs.String(); out != "" {
		t.Errorf("no configured embedder must warn nothing: %q", out)
	}
}

// TestProbeEmbedder_NilEmbedder pins the nil guard directly.
func TestProbeEmbedder_NilEmbedder(t *testing.T) {
	if err := probeEmbedder(t.Context(), nil); err != nil {
		t.Errorf("probeEmbedder(nil) = %v, want nil", err)
	}
}

// probeAttached mimics the wiring path (yaah-ka6 review): the embedder is
// built once and the startup probe runs against that same attached instance,
// so a gguf embedder cannot load its model twice or leak a probe-only copy.
func probeAttached(cfg *config.Config) bool {
	e, _ := embedderFor(cfg)
	return checkEmbedderAtStartup(e, cfg)
}

// TestCheckEmbedderAtStartup_SlowServerIsNotUnreachable covers the ka6
// review's cold-start case: a server that answers after the sync wait must
// not be reported unreachable at startup.
func TestCheckEmbedderAtStartup_SlowServerIsNotUnreachable(t *testing.T) {
	logs := captureProbeLogs(t)
	oldWait := embedProbeSyncWait
	embedProbeSyncWait = 10 * time.Millisecond
	t.Cleanup(func() { embedProbeSyncWait = oldWait })

	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float64{0.1, 0.2, -0.3}}},
		})
	}))
	defer srv.Close()

	if reachable := probeAttached(probeCfg(srv.URL)); !reachable {
		t.Error("a slow-but-alive embedder must not be reported unreachable")
	}
	if out := logs.String(); strings.Contains(out, "unreachable") {
		t.Errorf("startup must not label a slow server unreachable: %q", out)
	}
	unblock()                          // the server answers late
	time.Sleep(200 * time.Millisecond) // let the background probe observe it
	if out := logs.String(); out != "" {
		t.Errorf("late successful probe must not warn: %q", out)
	}
}
