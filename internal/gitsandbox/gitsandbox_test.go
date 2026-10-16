package gitsandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	shepherdsandbox "github.com/buchenberg/shepherd-kernel-go/sandbox"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"
)

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func newRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitCmd(t, repo, "init", "-b", "main")
	writeFile(t, repo, "tracked.txt", "base\n")
	writeFile(t, repo, ".gitignore", "ignored/\n")
	gitCmd(t, repo, "add", "-A")
	gitCmd(t, repo, "commit", "-m", "base")
	return repo
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func mustCapture(t *testing.T, sb shepherdsandbox.Sandbox) shepherdtrace.WorkspaceState {
	t.Helper()
	ws, err := sb.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	return ws
}

func mustApply(t *testing.T, sb shepherdsandbox.Sandbox, ws shepherdtrace.WorkspaceState) {
	t.Helper()
	if err := sb.Apply(context.Background(), ws); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

// The t60/#313 regression: an untracked file and a whole untracked
// directory created AFTER the checkpoint must survive the rewind, while
// tracked modifications are still reverted.
func TestApply_PreservesUncapturedUntracked(t *testing.T) {
	repo := newRepo(t)
	sb := NewLocalSandbox(repo)
	ws := mustCapture(t, sb)

	writeFile(t, repo, "tracked.txt", "unit edit\n")
	writeFile(t, repo, "internal/demo/roman.go", "package demo\n")
	writeFile(t, repo, ".agents/roles/demo-scribe.md", "role\n")

	mustApply(t, sb, ws)

	if got := readFile(t, repo, "tracked.txt"); got != "base\n" {
		t.Errorf("tracked.txt = %q, want the captured content", got)
	}
	if got := readFile(t, repo, "internal/demo/roman.go"); got != "package demo\n" {
		t.Errorf("post-checkpoint untracked file lost: %q", got)
	}
	if got := readFile(t, repo, ".agents/roles/demo-scribe.md"); got != "role\n" {
		t.Errorf("post-checkpoint untracked role file lost: %q", got)
	}
}

// Untracked content that WAS captured is part of the checkpoint tree and
// must round-trip through Apply with its captured content.
func TestApply_RestoresCapturedUntracked(t *testing.T) {
	repo := newRepo(t)
	writeFile(t, repo, "scratch.txt", "captured\n")
	sb := NewLocalSandbox(repo)
	ws := mustCapture(t, sb)

	writeFile(t, repo, "scratch.txt", "clobbered\n")
	mustApply(t, sb, ws)

	if got := readFile(t, repo, "scratch.txt"); got != "captured\n" {
		t.Errorf("captured untracked file = %q, want %q", got, "captured\n")
	}
}

// The evacuation stash lives inside .git and must not leak.
func TestApply_LeavesNoStashResidue(t *testing.T) {
	repo := newRepo(t)
	sb := NewLocalSandbox(repo)
	ws := mustCapture(t, sb)
	writeFile(t, repo, "new/dir/file.txt", "x\n")
	mustApply(t, sb, ws)

	entries, err := os.ReadDir(filepath.Join(repo, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "shepherd-untracked-") {
			t.Errorf("stash dir leaked into .git: %s", e.Name())
		}
	}
}

// gitignored content is outside the untracked listing and must survive
// untouched (the kernel clean never used -x; the wrapper must not either).
func TestApply_IgnoredFilesUntouched(t *testing.T) {
	repo := newRepo(t)
	sb := NewLocalSandbox(repo)
	ws := mustCapture(t, sb)

	writeFile(t, repo, "ignored/data.bin", "precious\n")
	mustApply(t, sb, ws)

	if got := readFile(t, repo, "ignored/data.bin"); got != "precious\n" {
		t.Errorf("ignored file lost: %q", got)
	}
}

// A failed apply must not strand evacuated files in the stash.
func TestApply_FailedApplyRestoresEvacuated(t *testing.T) {
	repo := newRepo(t)
	sb := NewLocalSandbox(repo)
	ws := mustCapture(t, sb)
	writeFile(t, repo, "precious.txt", "keep me\n")

	broken := ws
	broken.Data = map[string]any{}
	for k, v := range ws.Data {
		broken.Data[k] = v
	}
	broken.Data["head_sha"] = strings.Repeat("ab", 20) // resolvable-shaped, nonexistent

	err := sb.Apply(context.Background(), broken)
	if err == nil {
		t.Fatal("expected Apply with a bogus head_sha to fail")
	}
	if got := readFile(t, repo, "precious.txt"); got != "keep me\n" {
		t.Errorf("evacuated file stranded after failed apply: %q", got)
	}
}

// The kernel settlement type-asserts optional capabilities on the
// sandbox; the decorator must keep them reachable through promotion.
func TestNewLocalSandbox_KeepsOptionalCapabilities(t *testing.T) {
	sb := NewLocalSandbox(t.TempDir())
	if _, ok := sb.(shepherdsandbox.DeltaApplier); !ok {
		t.Error("DeltaApplier capability lost through the decorator")
	}
	if _, ok := sb.(shepherdsandbox.DeltaBaseAnnotator); !ok {
		t.Error("DeltaBaseAnnotator capability lost through the decorator")
	}
	if _, ok := sb.(shepherdsandbox.Addressed); !ok {
		t.Error("Addressed capability lost through the decorator")
	}
	if sb.Backend() != "git" {
		t.Errorf("Backend() = %q, want git", sb.Backend())
	}
}

// Capture must pass through untouched so checkpoint digests keep the
// kernel's stability contract.
func TestCapture_Passthrough(t *testing.T) {
	repo := newRepo(t)
	wrapped := NewLocalSandbox(repo)
	ws1 := mustCapture(t, wrapped)
	ws2 := mustCapture(t, wrapped)
	d1, err1 := ws1.Digest()
	d2, err2 := ws2.Digest()
	if err1 != nil || err2 != nil {
		t.Fatalf("Digest: %v / %v", err1, err2)
	}
	if d1 != d2 {
		t.Errorf("unchanged workspace captured different digests: %s vs %s", d1, d2)
	}
}
