package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	croft "github.com/buchenberg/croft"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/sandboxfake"
)

// gitRun runs one git command in dir and fails the test on a non-zero
// exit. It mirrors the per-test helpers above with the fixed author
// identity git needs to commit.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// gitHostRepo creates a temp git work tree with one committed file and one
// committed executable, and returns its path. The bootstrap requires a git
// source, so every bootstrap test starts here.
func gitHostRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	// Line endings must be byte-stable on every platform: a host git with
	// core.autocrlf=true (Git for Windows' default) rewrites patch
	// content to CRLF at apply time, and the byte-exact assertions below
	// would then compare against the runner's line-ending policy instead
	// of the patch.
	run("config", "core.autocrlf", "false")
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "base")
	return dir
}

func TestBootstrap_CopiesTreeAndCapturesBaseline(t *testing.T) {
	src := gitHostRepo(t)
	open, _ := cleanOpener()

	ss, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, src, nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	defer ss.Destroy(context.Background())

	ws := ss.Workspace()
	for name, want := range map[string]string{
		"readme.txt": "hello\n",
		"pkg/run.sh": "#!/bin/sh\n",
	} {
		p, err := ws.ResolvePath(name)
		if err != nil {
			t.Fatalf("ResolvePath(%s): %v", name, err)
		}
		got, err := ws.ReadFile(context.Background(), p)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	// The copy-in preserves the executable bit. (stat -c, not test -x:
	// the fake's shell implements the GNU stat surface the workspace
	// itself relies on.) Windows cannot represent permission bits — every
	// regular file the host reports is 0666 there — so the assertion only
	// runs where they exist.
	if runtime.GOOS != "windows" {
		runPath, err := ws.ResolvePath("pkg/run.sh")
		if err != nil {
			t.Fatal(err)
		}
		res, err := ws.Exec(context.Background(), ExecRequest{Command: "sh", Args: []string{"-c", `stat -c %a "$1"`, "sh", runPath}})
		if err != nil {
			t.Fatalf("stat the copied script: %v", err)
		}
		if got := strings.TrimSpace(res.Stdout); got != "755" {
			t.Errorf("the copied script's mode = %s, want 755", got)
		}
	}

	// Right after the bootstrap, the tree is the baseline: nothing has
	// changed since the capture.
	if diff, files, err := ss.DiffSinceBaseline(context.Background(), 0); err != nil {
		t.Fatalf("DiffSinceBaseline: %v", err)
	} else if len(files) != 0 || diff != "" {
		t.Errorf("fresh bootstrap reports changes: %d files, diff %q", len(files), diff)
	}
	if ss.Baseline() == nil {
		t.Error("the bootstrap must record a baseline")
	}
}

func TestBootstrap_RequiresGitWorkTree(t *testing.T) {
	src := t.TempDir() // no .git: not a work tree
	// t.TempDir() lives wherever TMPDIR points, and git rev-parse walks
	// parent directories: on a machine whose temp dir sits inside a
	// checkout, this source would look like a work tree. Ceiling the
	// discovery walk at src itself makes "not a git work tree"
	// deterministic on every machine.
	t.Setenv("GIT_CEILING_DIRECTORIES", src)
	open, fakes := cleanOpener()

	_, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, src, nil)
	if err == nil {
		t.Fatal("a bootstrap from a non-git source must fail the session")
	}
	if !strings.Contains(err.Error(), "git work tree") {
		t.Errorf("error = %v, want it to name the git-work-tree requirement", err)
	}
	// The session sandbox was already created when the bootstrap refused:
	// it must have been destroyed, not leaked.
	opened := fakes()
	if len(opened) < 2 {
		t.Fatalf("opener calls = %d, want 2 (probe, then session sandbox)", len(opened))
	}
	if !opened[1].Destroyed() {
		t.Error("a failed bootstrap must destroy the session sandbox")
	}
}

// TestBootstrap_RollbackRestoresBaseline is D1's acceptance criterion: in
// isolated mode, a failed attempt must still be rollable back to the
// session baseline (plan §2.3). The supervised machinery applies states
// through the same kernel verb this test uses.
func TestBootstrap_RollbackRestoresBaseline(t *testing.T) {
	src := gitHostRepo(t)
	open, _ := cleanOpener()
	ss, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, src, nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	defer ss.Destroy(context.Background())

	ws := ss.Workspace()
	readme, _ := ws.ResolvePath("readme.txt")
	notes, _ := ws.ResolvePath("notes.txt")
	if err := ws.WriteFile(context.Background(), readme, []byte("mutated\n"), 0o644); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if err := ws.WriteFile(context.Background(), notes, []byte("stray\n"), 0o644); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if diff, files, _ := ss.DiffSinceBaseline(context.Background(), 0); len(files) == 0 || diff == "" {
		t.Fatal("mutations are not visible against the baseline")
	}

	// Rollback: re-apply the baseline, exactly as the supervised session
	// would on a failed attempt.
	if err := ss.sb.Apply(context.Background(), *ss.Baseline()); err != nil {
		t.Fatalf("Apply(baseline): %v", err)
	}
	if diff, files, err := ss.DiffSinceBaseline(context.Background(), 0); err != nil {
		t.Fatalf("DiffSinceBaseline after rollback: %v", err)
	} else if len(files) != 0 || diff != "" {
		t.Errorf("rollback left changes behind: %d files, diff %q", len(files), diff)
	}
}

// TestApplyOut_CopyOutFallback walks the full D1 rhythm against a backend
// without delta machinery (the fake): copy in, mutate in the container —
// modify, add, and delete — then apply out and find all three on the host.
func TestApplyOut_CopyOutFallback(t *testing.T) {
	src := gitHostRepo(t)
	open, _ := cleanOpener()
	ss, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, src, nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	defer ss.Destroy(context.Background())

	ws := ss.Workspace()
	readme, _ := ws.ResolvePath("readme.txt")
	result, _ := ws.ResolvePath("result.txt")
	if err := ws.WriteFile(context.Background(), readme, []byte("applied out\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteFile(context.Background(), result, []byte("new from the container\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script, _ := ws.ResolvePath("pkg/run.sh")
	if err := ws.Remove(context.Background(), script); err != nil {
		t.Fatal(err)
	}

	if err := ss.ApplyOut(context.Background(), src); err != nil {
		t.Fatalf("ApplyOut: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(src, "readme.txt")); err != nil || string(got) != "applied out\n" {
		t.Errorf("host readme.txt = %q, %v — the in-container modification did not land", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(src, "result.txt")); err != nil || string(got) != "new from the container\n" {
		t.Errorf("host result.txt = %q, %v — the new file did not land", got, err)
	}
	if _, err := os.Lstat(filepath.Join(src, "pkg", "run.sh")); !os.IsNotExist(err) {
		t.Errorf("host pkg/run.sh still exists (%v) — the in-container deletion did not land", err)
	}
	// Directories the deletion emptied stay behind on the host: the fake
	// reports the file's path, and only paths the sandbox still holds are
	// recreated. pkg/ itself remains, which is correct — the sandbox still
	// has it.
	if _, err := os.Stat(filepath.Join(src, "pkg")); err != nil {
		t.Errorf("pkg/ should remain on the host: %v", err)
	}
}

func TestApplyOut_GitPatch(t *testing.T) {
	dir := gitHostRepo(t)
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	// Change a file, add a binary file, and generate the binary patch.
	// Staging first mirrors the kernel's delta script: git diff alone
	// would miss the untracked new file.
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("changed on the branch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0x00, 0x00, 0x7f}
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), bin, 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	patch := run("diff", "--binary", "--cached", "HEAD")

	// Reset the tree, then apply the patch the way ApplyOut's containerd
	// path does.
	run("reset", "-q", "--hard", "HEAD")
	run("clean", "-q", "-fd")
	if err := applyGitPatch(context.Background(), dir, []byte(patch)); err != nil {
		t.Fatalf("applyGitPatch: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "readme.txt")); err != nil || string(got) != "changed on the branch\n" {
		t.Errorf("readme.txt = %q, %v — the patch did not apply", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "blob.bin")); err != nil || !bytes.Equal(got, bin) {
		t.Errorf("blob.bin = %x, %v — the binary payload did not survive (want %x)", got, err, bin)
	}
}

// deltaStubSandbox wraps the fake and answers Capture from a canned state,
// emulating the containerd backend's delta-carrying captures without a
// daemon.
type deltaStubSandbox struct {
	croft.Sandbox
	state croft.WorkspaceState
}

func (d *deltaStubSandbox) Capture(context.Context) (croft.WorkspaceState, error) {
	return d.state, nil
}

func TestApplyOut_PrefersBackendDeltaPatch(t *testing.T) {
	dir := gitHostRepo(t)
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("via delta patch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := run("diff", "--binary", "HEAD")
	run("reset", "-q", "--hard", "HEAD")

	stub := &deltaStubSandbox{Sandbox: sandboxfake.New(), state: croft.WorkspaceState{
		Backend: "fake",
		Data: map[string]any{
			"delta_base":  "head0",
			"delta_patch": base64.StdEncoding.EncodeToString([]byte(patch)),
		},
	}}
	ss := &SessionSandbox{
		sb:       stub,
		ws:       newSandboxWorkspace(stub, "/workspace"),
		baseline: &croft.WorkspaceState{Backend: "fake"},
	}

	if err := ss.ApplyOut(context.Background(), dir); err != nil {
		t.Fatalf("ApplyOut: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "readme.txt")); err != nil || string(got) != "via delta patch\n" {
		t.Errorf("readme.txt = %q, %v — the backend's delta patch was not preferred", got, err)
	}
}

// diffFailingSandbox pins the branch a test claims to exercise: its Diff
// always fails, so a fallback that consults the diff fails the test instead
// of quietly passing through a second no-op path.
type diffFailingSandbox struct {
	croft.Sandbox
}

func (d *diffFailingSandbox) Diff(context.Context, croft.WorkspaceState, int) (string, []string, error) {
	return "", nil, errors.New("the copy-out fallback ran: the delta-base branch was not recognised")
}

func TestApplyOut_CleanDeltaIsNoop(t *testing.T) {
	dir := gitHostRepo(t)
	// The fake's Diff on this stub reports nothing and touches nothing,
	// so without the failing wrapper a missed delta-base match would
	// still pass — the exact regression this test exists to catch.
	stub := &deltaStubSandbox{Sandbox: &diffFailingSandbox{Sandbox: sandboxfake.New()}, state: croft.WorkspaceState{
		Backend: "fake",
		Data:    map[string]any{"delta_base": "head0"}, // base present, no patch: clean
	}}
	ss := &SessionSandbox{
		sb:       stub,
		ws:       newSandboxWorkspace(stub, "/workspace"),
		baseline: &croft.WorkspaceState{Backend: "fake"},
	}

	if err := ss.ApplyOut(context.Background(), dir); err != nil {
		t.Fatalf("ApplyOut on a clean capture = %v, want nil", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "readme.txt")); err != nil || string(got) != "hello\n" {
		t.Errorf("a clean apply-out touched the host: readme.txt = %q, %v", got, err)
	}
}

func TestApplyOut_RefusesWithoutBaseline(t *testing.T) {
	open, _ := cleanOpener()
	ss, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, "", nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	defer ss.Destroy(context.Background())

	if err := ss.ApplyOut(context.Background(), t.TempDir()); err == nil {
		t.Error("ApplyOut without a bootstrap must refuse, not silently no-op")
	}
	if _, _, err := ss.DiffSinceBaseline(context.Background(), 0); err == nil {
		t.Error("DiffSinceBaseline without a baseline must refuse")
	}
}

// The bootstrap's source checks: each layout below copies in but leaves
// the container without a usable in-container git, so each must fail the
// session at startup instead of breaking capture, diff, or apply-out
// mid-session.

func TestBootstrap_RejectsSubdirectory(t *testing.T) {
	src := filepath.Join(gitHostRepo(t), "pkg")
	open, _ := cleanOpener()

	_, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, src, nil)
	if err == nil {
		t.Fatal("a bootstrap from a repository subdirectory must fail the session")
	}
	if !strings.Contains(err.Error(), "top level") {
		t.Errorf("error = %v, want it to name the top-level requirement", err)
	}
}

func TestBootstrap_RejectsLinkedWorktree(t *testing.T) {
	dir := gitHostRepo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	gitRun(t, dir, "worktree", "add", "-q", linked, "HEAD")
	open, _ := cleanOpener()

	_, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, linked, nil)
	if err == nil {
		t.Fatal("a bootstrap from a linked worktree must fail the session")
	}
	if !strings.Contains(err.Error(), "self-contained") {
		t.Errorf("error = %v, want it to name the self-contained-repository requirement", err)
	}
}

func TestBootstrap_RejectsTrackedSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("git on this platform stores symlinks as plain files, so the tracked-symlink case cannot arise")
	}
	src := gitHostRepo(t)
	if err := os.Symlink("readme.txt", filepath.Join(src, "link")); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}
	gitRun(t, src, "add", "link")
	gitRun(t, src, "commit", "-q", "-m", "link")
	open, _ := cleanOpener()

	_, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, src, nil)
	if err == nil {
		t.Fatal("a bootstrap from a tree with a tracked symlink must fail the session")
	}
	if !strings.Contains(err.Error(), "tracked symlink") {
		t.Errorf("error = %v, want it to name the tracked symlink", err)
	}
}

// The deny policy at the copy-in boundary: an untracked secret never
// enters the container, and a tracked one fails loudly rather than being
// excluded (its absence would become a host-side deletion at apply-out).

func TestBootstrap_LeavesDeniedFilesOut(t *testing.T) {
	src := gitHostRepo(t)
	if err := os.WriteFile(filepath.Join(src, ".env"), []byte("SECRET=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	open, _ := cleanOpener()

	ss, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, src, []string{".env"})
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	defer ss.Destroy(context.Background())

	ws := ss.Workspace()
	p, err := ws.ResolvePath(".env")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.ReadFile(context.Background(), p); err == nil {
		t.Error("a denied file must not be readable inside the sandbox")
	}
	if got, err := os.ReadFile(filepath.Join(src, ".env")); err != nil || string(got) != "SECRET=1\n" {
		t.Errorf("the host .env = %q, %v — excluding it from the sandbox must not touch the host", got, err)
	}
	// The exclusion is symmetric (absent in the container, untracked on
	// the host), so the baseline stays clean: no diff, no container-side
	// git, and apply-out has nothing spurious to apply.
	if diff, files, err := ss.DiffSinceBaseline(context.Background(), 0); err != nil {
		t.Fatalf("DiffSinceBaseline: %v", err)
	} else if len(files) != 0 || diff != "" {
		t.Errorf("the excluded file leaked into the baseline: %d files, diff %q", len(files), diff)
	}
}

func TestBootstrap_RejectsTrackedDeniedFile(t *testing.T) {
	src := gitHostRepo(t)
	if err := os.WriteFile(filepath.Join(src, ".env"), []byte("SECRET=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, src, "add", ".env")
	gitRun(t, src, "commit", "-q", "-m", "secret")
	open, _ := cleanOpener()

	_, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, src, []string{".env"})
	if err == nil {
		t.Fatal("a bootstrap from a tree with a tracked denied file must fail the session")
	}
	if !strings.Contains(err.Error(), "deny pattern") {
		t.Errorf("error = %v, want it to name the deny pattern", err)
	}
}

// gitRecordingSandbox answers in-container git commands with success and
// records them. The fake's shell has no git, and what is under test is
// whether the bootstrap runs the synthetic baseline commit — not what a
// real container's git would do with one.
type gitRecordingSandbox struct {
	croft.Sandbox
	mu       sync.Mutex
	gitCalls []string
}

func (g *gitRecordingSandbox) Exec(ctx context.Context, req croft.ExecRequest) (croft.ExecResult, error) {
	if req.Command == "git" {
		g.mu.Lock()
		g.gitCalls = append(g.gitCalls, strings.Join(req.Args, " "))
		g.mu.Unlock()
		return croft.ExecResult{ExitCode: 0}, nil
	}
	return g.Sandbox.Exec(ctx, req)
}

func (g *gitRecordingSandbox) calls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.gitCalls...)
}

// recordingOpener hands the probe a hook-wired fake (its shell cannot run
// the contract script) and the session sandbox the given recorder.
func recordingOpener(rec croft.Sandbox) SandboxOpener {
	var opened int
	return func(config.WorkspaceConfig) (croft.Sandbox, error) {
		opened++
		if opened == 1 {
			sb := sandboxfake.New()
			sb.ExecHook = func(croft.ExecRequest) (croft.ExecResult, error) {
				return croft.ExecResult{ExitCode: 0}, nil
			}
			return sb, nil
		}
		return rec, nil
	}
}

func TestBootstrap_DirtySourceCommitsBaselineInContainer(t *testing.T) {
	src := gitHostRepo(t)
	if err := os.WriteFile(filepath.Join(src, "readme.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &gitRecordingSandbox{Sandbox: sandboxfake.New()}

	ss, err := StartSessionSandbox(context.Background(), sessionTestConfig(), recordingOpener(rec), src, nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	defer ss.Destroy(context.Background())

	calls := rec.calls()
	if len(calls) != 2 || calls[0] != "add -A" || !strings.Contains(calls[1], "commit") {
		t.Errorf("container git calls = %q, want the synthetic baseline (add -A, then commit)", calls)
	}
}

func TestBootstrap_CleanSourceRunsNoContainerGit(t *testing.T) {
	src := gitHostRepo(t)
	rec := &gitRecordingSandbox{Sandbox: sandboxfake.New()}

	ss, err := StartSessionSandbox(context.Background(), sessionTestConfig(), recordingOpener(rec), src, nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	defer ss.Destroy(context.Background())

	if calls := rec.calls(); len(calls) != 0 {
		t.Errorf("container git calls = %q, want none — a clean tree already matches HEAD", calls)
	}
}
