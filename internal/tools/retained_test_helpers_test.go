package tools

// retained_test_helpers_test.go: the fixtures the retained-lane tests share. The
// retained lane used to borrow them from the supervised-task tests; with that
// tool gone they live here, trimmed to what the retained tests need.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	shepherdtrace "github.com/buchenberg/shepherd-kernel-go"
	shepherd "github.com/buchenberg/shepherd-runtime-go"

	"github.com/buchenberg/yaah/internal/gitsandbox"
)

type sessionFixtures struct {
	mgr   *shepherd.ScopeManager
	store *shepherdtrace.SQLiteTraceStore
}

// newSessionFixtures builds the shared fixtures: an in-memory trace store with
// its scope manager, a real temp git repo, and a root scope over it.
func newSessionFixtures(t *testing.T) (*sessionFixtures, string, string) {
	t.Helper()

	store, err := shepherdtrace.NewSQLiteTraceStore(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })

	fx := &sessionFixtures{mgr: shepherd.NewScopeManager(store), store: store}

	repo := newTestGitRepo(t)

	root, err := fx.mgr.Create("test-session-root", gitsandbox.NewLocalSandbox(repo))
	if err != nil {
		t.Fatalf("create root scope: %v", err)
	}

	return fx, repo, root.ID()
}

func newTestGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}

	dir := t.TempDir()

	// Isolate from the developer's global/system git config so inherited
	// settings (gpgsign, hooksPath, templates) cannot break the test.
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "gitconfig-system"))

	mustRunGit(t, dir, "init")
	mustRunGit(t, dir, "config", "user.email", "test@test.com")
	mustRunGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	mustRunGit(t, dir, "add", "-A")
	mustRunGit(t, dir, "commit", "-m", "init")
	return dir
}

func mustRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

type runnerResponse struct {
	result         string
	err            error
	waitForContext bool
}

// sessionRunner replays configured responses and records every dispatch, so a
// test can assert where the sub-agent actually ran.
type sessionRunner struct {
	mu        sync.Mutex
	responses []runnerResponse
	prompts   []string
	workdirs  []string
	// sideEffect runs before each response is returned so tests can mutate the
	// workspace like a real sub-agent would.
	sideEffect func(call int, repoPath string)
	repoPath   string
}

func (f *sessionRunner) run() TaskRunner {
	return func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
		f.mu.Lock()
		idx := len(f.prompts)
		f.prompts = append(f.prompts, prompt)
		f.workdirs = append(f.workdirs, params.Workdir)
		resp := runnerResponse{result: "default"}
		if idx < len(f.responses) {
			resp = f.responses[idx]
		}
		f.mu.Unlock()

		if f.sideEffect != nil {
			f.sideEffect(idx, f.repoPath)
		}
		if resp.waitForContext {
			<-ctx.Done()
			return resp.result, ctx.Err()
		}
		return resp.result, resp.err
	}
}

// workdirAt is workdirFor without the testing dependency, for use inside a
// dispatch goroutine — t.Fatalf is only legal on the test's own goroutine.
func (f *sessionRunner) workdirAt(call int) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if call >= len(f.workdirs) {
		return "", false
	}
	return f.workdirs[call], true
}

func (f *sessionRunner) workdirFor(t *testing.T, call int) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if call >= len(f.workdirs) {
		t.Fatalf("no workdir recorded for call %d (have %d)", call, len(f.workdirs))
	}
	return f.workdirs[call]
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
