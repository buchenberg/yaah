package gitsandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
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
	// Isolate from the developer's global/system git config (Windows
	// runners default core.autocrlf=true, which rewrites checked-out
	// content to CRLF and breaks byte-exact assertions).
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "gitconfig-system"))
	gitCmd(t, repo, "init", "-b", "main")
	gitCmd(t, repo, "config", "core.autocrlf", "false")
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

func mustCapture(t *testing.T, sb goatherdsandbox.Sandbox) shepherdtrace.WorkspaceState {
	t.Helper()
	ws, err := sb.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	return ws
}

func mustApply(t *testing.T, sb goatherdsandbox.Sandbox, ws shepherdtrace.WorkspaceState) {
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
	if _, ok := sb.(goatherdsandbox.DeltaApplier); !ok {
		t.Error("DeltaApplier capability lost through the decorator")
	}
	if _, ok := sb.(goatherdsandbox.DeltaBaseAnnotator); !ok {
		t.Error("DeltaBaseAnnotator capability lost through the decorator")
	}
	if _, ok := sb.(goatherdsandbox.Addressed); !ok {
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

// findStash locates a retained shepherd-untracked stash dir under .git.
func findStash(t *testing.T, repo string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repo, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "shepherd-untracked-") {
			return filepath.Join(repo, ".git", e.Name())
		}
	}
	return ""
}

// Review finding: when the applied state puts a FILE where a preserved
// untracked DIRECTORY stood, the restore must fail loudly and keep the
// only copy of the content in the stash — never delete it and report
// success.
func TestApply_FileDirConflictKeepsStash(t *testing.T) {
	repo := newRepo(t)
	writeFile(t, repo, "scratch", "i am a file\n")
	gitCmd(t, repo, "add", "-A")
	gitCmd(t, repo, "commit", "-m", "scratch file")

	sb := NewLocalSandbox(repo)
	ws := mustCapture(t, sb)

	// Replace the tracked file with a directory holding uncaptured content.
	if err := os.Remove(filepath.Join(repo, "scratch")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "scratch/notes.txt", "precious notes\n")

	err := sb.Apply(context.Background(), ws)
	if err == nil {
		t.Fatal("apply must report the failed restore, not succeed silently")
	}
	if !strings.Contains(err.Error(), "kept") {
		t.Errorf("error must point at the retained stash: %v", err)
	}
	if got := readFile(t, repo, "scratch"); got != "i am a file\n" {
		t.Errorf("tracked file not restored: %q", got)
	}
	stash := findStash(t, repo)
	if stash == "" {
		t.Fatal("stash must be retained when a restore fails")
	}
	if got, rerr := os.ReadFile(filepath.Join(stash, "scratch", "notes.txt")); rerr != nil || string(got) != "precious notes\n" {
		t.Errorf("conflicted original lost from stash: %q err %v", got, rerr)
	}
}

// Review finding: restoration must not follow symlinks in destination
// parents — a checkpoint that reinstates `scratch -> ../outside` must
// not let a preserved `scratch/notes.txt` escape the repository.
func TestApply_SymlinkParentNotFollowed(t *testing.T) {
	repo := newRepo(t)
	link := filepath.Join(repo, "scratch")
	if err := os.Symlink("../outside", link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	gitCmd(t, repo, "config", "core.symlinks", "true")
	gitCmd(t, repo, "add", "-A")
	gitCmd(t, repo, "commit", "-m", "scratch symlink")
	// Verify git really stored a symlink; on hosts where it degraded to
	// a regular file the scenario is not the one under test.
	if out := gitCmd(t, repo, "ls-files", "-s", "scratch"); !strings.Contains(out, "120000") {
		t.Skip("git did not store a symlink (core.symlinks off)")
	}

	sb := NewLocalSandbox(repo)
	ws := mustCapture(t, sb)

	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "scratch/notes.txt", "precious notes\n")
	if err := os.MkdirAll(filepath.Join(repo, "..", "outside"), 0o755); err == nil {
		t.Cleanup(func() { os.RemoveAll(filepath.Join(repo, "..", "outside")) })
	}

	err := sb.Apply(context.Background(), ws)
	if err == nil {
		t.Fatal("apply must report the refused restore through the symlinked parent")
	}
	if _, serr := os.Lstat(link); serr != nil {
		t.Errorf("checkpoint symlink not reinstated: %v", serr)
	}
	if _, serr := os.Lstat(filepath.Join(repo, "..", "outside", "notes.txt")); serr == nil {
		t.Error("preserved file escaped the repository through the symlinked parent")
	}
	stash := findStash(t, repo)
	if stash == "" {
		t.Fatal("stash must be retained when a restore is refused")
	}
	if got, rerr := os.ReadFile(filepath.Join(stash, "scratch", "notes.txt")); rerr != nil || string(got) != "precious notes\n" {
		t.Errorf("refused original lost from stash: %q err %v", got, rerr)
	}
}

// Review finding: `git ls-files --others` never lists empty directories,
// so empty untracked dirs (and empty descendants of populated ones)
// must be preserved by shape, or `git clean -fd` still deletes them —
// the directory-loss half of #313.
func TestApply_PreservesEmptyDirs(t *testing.T) {
	repo := newRepo(t)
	sb := NewLocalSandbox(repo)
	ws := mustCapture(t, sb)

	if err := os.MkdirAll(filepath.Join(repo, "newdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "newdir2", "emptysub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "newdir2/f.txt", "x\n")

	mustApply(t, sb, ws)

	for _, d := range []string{"newdir", "newdir2", "newdir2/emptysub"} {
		fi, err := os.Stat(filepath.Join(repo, d))
		if err != nil || !fi.IsDir() {
			t.Errorf("untracked directory %s lost by rewind: err %v", d, err)
		}
	}
	if got := readFile(t, repo, "newdir2/f.txt"); got != "x\n" {
		t.Errorf("newdir2/f.txt = %q, want %q", got, "x\n")
	}
}
