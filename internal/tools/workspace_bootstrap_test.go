package tools

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
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

// TestBootstrap_UnbornHeadAccepted pins the snapshot-native bootstrap's
// ability to start from a freshly initialized repository: there is no HEAD to
// resolve, but the snapshot baseline does not need one.
func TestBootstrap_UnbornHeadAccepted(t *testing.T) {
	src := t.TempDir()
	if out, err := exec.Command("git", "-C", src, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	open, _ := cleanOpener()
	ss, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, src, nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	defer ss.Destroy(context.Background())
	if ss.Baseline() == nil {
		t.Fatal("an unborn-HEAD repository must still capture a baseline")
	}
	got, err := ss.Workspace().ReadFile(context.Background(), "/workspace/README.md")
	if err != nil || string(got) != "hello\n" {
		t.Fatalf("copied README = %q, %v", got, err)
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
// session baseline (plan §2.3). Turn checkpointing applies states
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

	// Rollback: re-apply the baseline, exactly as a failed isolated
	// attempt would be rolled back.
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

// TestApplyOut_CarriesModeChanges pins the mode half of apply-out fidelity,
// which nothing else asserted.
//
// Two cases, because they take different paths through the write:
//
//   - A mode change to a file that already exists on the host. This is the one
//     that silently regressed when the git-binary-patch path was removed:
//     atomicWriteFile preserves an existing file's mode by design, so without
//     copyOut's explicit chmod the host kept the old mode and the session's
//     chmod vanished on accept. The content is deliberately identical, so a
//     pass cannot be explained by a content write happening to set the mode.
//   - A new executable. The doc has always claimed "a new executable arrives
//     executable"; this is where that claim is actually checked.
//
// setuid/setgid/sticky are deliberately not carried: perm is masked with
// .Perm(), so a container-authored privilege bit never reaches the host. The
// asymmetry is intentional (yaah-vlwm); TestApplyOut_DoesNotPropagateSetuid
// pins it explicitly.
func TestApplyOut_CarriesModeChanges(t *testing.T) {
	// Windows cannot represent permission bits — every regular file the host
	// reports is 0666 there, and a chmod cannot make it 0755 — so there is no
	// mode to carry and the property is unobservable. The bootstrap test above
	// guards its own mode assertion the same way.
	//
	// This skips the assertion, not the code: copyOut's Chmod still runs on
	// Windows (TestApplyOut_CopyOutFallback exercises it and passes there), so
	// os.Chmod is not a new failure mode on that platform.
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot represent permission bits, so a mode-only change is not observable")
	}

	src := gitHostRepo(t)
	hostReadme := filepath.Join(src, "readme.txt")
	before, err := os.Stat(hostReadme)
	if err != nil {
		t.Fatalf("stat host readme.txt: %v", err)
	}
	if before.Mode().Perm() != 0o644 {
		t.Fatalf("the fixture's readme.txt is %v, want 0644 — the mode-change case needs a known starting mode", before.Mode().Perm())
	}

	open, _ := cleanOpener()
	ss, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, src, nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	defer ss.Destroy(context.Background())

	ws := ss.Workspace()
	readme, _ := ws.ResolvePath("readme.txt")
	script, _ := ws.ResolvePath("bin/deploy.sh")

	// Same bytes, new mode: a mode-only change.
	if err := ws.WriteFile(context.Background(), readme, []byte("hello\n"), 0o755); err != nil {
		t.Fatalf("chmod-equivalent write: %v", err)
	}
	if err := ws.WriteFile(context.Background(), script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write new executable: %v", err)
	}

	if err := ss.ApplyOut(context.Background(), src); err != nil {
		t.Fatalf("ApplyOut: %v", err)
	}

	got, err := os.Stat(hostReadme)
	if err != nil {
		t.Fatalf("stat host readme.txt after apply-out: %v", err)
	}
	if got.Mode().Perm() != 0o755 {
		t.Errorf("host readme.txt mode = %v, want 0755 — a mode-only change made in the session was dropped on accept", got.Mode().Perm())
	}
	if body, rerr := os.ReadFile(hostReadme); rerr != nil || string(body) != "hello\n" {
		t.Errorf("host readme.txt content = %q, %v — want the unchanged %q", body, rerr, "hello\n")
	}

	gotScript, err := os.Stat(filepath.Join(src, "bin", "deploy.sh"))
	if err != nil {
		t.Fatalf("stat the new executable after apply-out: %v", err)
	}
	if gotScript.Mode().Perm() != 0o755 {
		t.Errorf("new file mode = %v, want 0755 — a new executable must arrive executable", gotScript.Mode().Perm())
	}
}

// TestApplyOut_DoesNotPropagateSetuid pins the deliberate asymmetry: copy-in
// carries the full mode, but copy-out masks it with .Perm(). A container is a
// weaker boundary, and a setuid/setgid/sticky bit authored there must not land
// on the host with host privilege semantics.
func TestApplyOut_DoesNotPropagateSetuid(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot represent permission or privilege bits")
	}

	src := gitHostRepo(t)
	open, _ := cleanOpener()
	ss, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, src, nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	defer ss.Destroy(context.Background())

	ws := ss.Workspace()
	readme, _ := ws.ResolvePath("readme.txt")
	if err := ws.WriteFile(context.Background(), readme, []byte("hello\n"), 0o7755); err != nil {
		t.Fatalf("write privileged mode in the sandbox: %v", err)
	}
	if err := ss.ApplyOut(context.Background(), src); err != nil {
		t.Fatalf("ApplyOut: %v", err)
	}

	got, err := os.Stat(filepath.Join(src, "readme.txt"))
	if err != nil {
		t.Fatalf("stat host readme.txt after apply-out: %v", err)
	}
	if got.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		t.Errorf("host readme.txt mode = %v: a container-authored privilege bit crossed to the host", got.Mode())
	}
	if got.Mode().Perm() != 0o755 {
		t.Errorf("host readme.txt mode = %v, want 0755 after masking privilege bits", got.Mode().Perm())
	}
}

// TestApplyOut_NeverImposesASymlinkModeOnTheHost pins a security boundary in
// copy-out: when the sandbox cannot report a file's mode, nothing is imposed on
// the host.
//
// The hazard is concrete. copyOut used to fall back to os.Lstat(host) when the
// sandbox Stat failed, and Lstat on a symlink reports the *link's* mode — 0777
// on POSIX — while atomicWriteFile replaces that path with a regular file. For
// a dangling symlink atomicWriteFile's own os.Stat fails as well, so it keeps
// the caller's perm and 0777 lands on a regular file: world-writable and
// world-executable on the host. A chmod added later made it worse by applying
// the same guess even where atomicWriteFile would have re-derived a safe mode.
//
// Both are gone: the fallback is removed, and the chmod runs only on a mode
// that came from the sandbox. This test pins the *fallback* half — with a
// dangling symlink the old code yielded 0777 here. It deliberately does not
// claim to pin the gate: once the fallback is gone, perm is the 0o644 default
// either way, so the chmod is a no-op on this fixture and removing the gate
// would not fail this test.
// TestApplyOut_DoesNotLoosenAPrivateHostFile covers the gate, in the direction
// that matters more — imposing a guessed default over a file the user had kept
// private.
func TestApplyOut_NeverImposesASymlinkModeOnTheHost(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a symlink reporting 0777 via Lstat is a POSIX property; Windows has no equivalent mode")
	}

	src := gitHostRepo(t)
	open, fakes := cleanOpener()
	ss, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, src, nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	defer ss.Destroy(context.Background())

	ws := ss.Workspace()
	readme, _ := ws.ResolvePath("readme.txt")
	if err := ws.WriteFile(context.Background(), readme, []byte("from the session\n"), 0o644); err != nil {
		t.Fatalf("write in the sandbox: %v", err)
	}

	// Replace the host path with a dangling symlink. Lstat reports the link's
	// own mode and Stat fails, so neither copyOut nor atomicWriteFile can derive
	// a real mode — the exact combination that used to impose a guess on the
	// host.
	//
	// The link's mode is read rather than assumed: Linux reports 0777, macOS
	// reports 0755. Both are wrong to impose, but only Linux's is
	// world-writable, so the test asserts the general property (the link's mode
	// is never what lands) and the world-writable symptom on top.
	hostReadme := filepath.Join(src, "readme.txt")
	if err := os.Remove(hostReadme); err != nil {
		t.Fatalf("remove the host file: %v", err)
	}
	if err := os.Symlink("no-such-target", hostReadme); err != nil {
		t.Fatalf("create the dangling symlink: %v", err)
	}
	link, lerr := os.Lstat(hostReadme)
	if lerr != nil {
		t.Fatalf("lstat the fixture symlink: %v", lerr)
	}
	linkMode := link.Mode().Perm()
	if linkMode == 0o644 {
		t.Fatalf("the fixture symlink's mode is %v, the same as the safe default — the test could not tell a guessed mode from a correct one", linkMode)
	}
	t.Logf("this platform reports a symlink as %v", linkMode)

	// Make the in-band stat fail, so copy-out has no authoritative mode. A
	// nonzero exit is how sandboxWorkspace.stat reports failure.
	for _, sb := range fakes() {
		sb.ExecHook = func(goatherdsandbox.ExecRequest) (goatherdsandbox.ExecResult, error) {
			return goatherdsandbox.ExecResult{ExitCode: 1, Stderr: "stat unavailable"}, nil
		}
	}

	if err := ss.ApplyOut(context.Background(), src); err != nil {
		t.Fatalf("ApplyOut: %v", err)
	}

	got, err := os.Lstat(hostReadme)
	if err != nil {
		t.Fatalf("stat the result: %v", err)
	}
	if got.Mode()&os.ModeSymlink != 0 {
		t.Fatal("the symlink should have been replaced by a regular file")
	}
	if got.Mode().Perm() == linkMode {
		t.Errorf("host file is %v — the symlink's own Lstat mode was imposed on a regular file, so copy-out guessed a mode it did not know", got.Mode().Perm())
	}
	if got.Mode().Perm()&0o022 != 0 {
		t.Errorf("host file is %v — group- or world-writable", got.Mode().Perm())
	}
	if body, rerr := os.ReadFile(hostReadme); rerr != nil || string(body) != "from the session\n" {
		t.Errorf("content = %q, %v — want the session's bytes", body, rerr)
	}
}

// TestApplyOut_DoesNotLoosenAPrivateHostFile pins the other half of the
// modeFromSandbox gate, and the direction that matters more.
//
// When the sandbox cannot report a file's mode, copy-out does not know it — so
// it must impose nothing. An ungated chmod would apply the 0o644 default over a
// host file the user had kept at 0o600, silently loosening a private file to
// world-readable on accept. atomicWriteFile would have preserved 0o600 on its
// own; the chmod is what overwrites that.
//
// The sibling symlink test cannot catch this: there the host path is a dangling
// symlink, so atomicWriteFile's Stat fails and the default is written with or
// without the chmod. Here the host file is a regular 0o600 file, so the two
// behaviours differ exactly — gated keeps 0o600, ungated yields 0o644.
func TestApplyOut_DoesNotLoosenAPrivateHostFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot represent permission bits, so 0600 vs 0644 is not observable")
	}

	src := gitHostRepo(t)
	hostSecret := filepath.Join(src, "secret.txt")
	if err := os.WriteFile(hostSecret, []byte("private\n"), 0o600); err != nil {
		t.Fatalf("write the private host file: %v", err)
	}
	// Explicit, because WriteFile's perm is subject to umask and the whole test
	// turns on the starting mode being exact. Git stores only the executable
	// bit, so committing does not disturb the 0600 in the working tree.
	if err := os.Chmod(hostSecret, 0o600); err != nil {
		t.Fatalf("chmod the fixture: %v", err)
	}
	// Commit it. The bootstrap copies the tree in file by file and the fake's
	// shell is not involved; committing keeps the fixture representative of a
	// normal repository source.
	gitRun(t, src, "add", "secret.txt")
	gitRun(t, src, "commit", "-q", "-m", "add a private file")

	if info, err := os.Stat(hostSecret); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the fixture needs a 0600 host file to be meaningful, got %v (%v)", info, err)
	}

	open, fakes := cleanOpener()
	ss, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, src, nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	defer ss.Destroy(context.Background())

	ws := ss.Workspace()
	secret, _ := ws.ResolvePath("secret.txt")
	// Edit the content so the path lands in the diff. The mode written here is
	// irrelevant: the sandbox Stat is about to fail, so copy-out never reads it.
	if err := ws.WriteFile(context.Background(), secret, []byte("edited in the session\n"), 0o600); err != nil {
		t.Fatalf("write in the sandbox: %v", err)
	}

	for _, sb := range fakes() {
		sb.ExecHook = func(goatherdsandbox.ExecRequest) (goatherdsandbox.ExecResult, error) {
			return goatherdsandbox.ExecResult{ExitCode: 1, Stderr: "stat unavailable"}, nil
		}
	}

	if err := ss.ApplyOut(context.Background(), src); err != nil {
		t.Fatalf("ApplyOut: %v", err)
	}

	got, err := os.Stat(hostSecret)
	if err != nil {
		t.Fatalf("stat the result: %v", err)
	}
	if got.Mode().Perm() != 0o600 {
		t.Errorf("host secret.txt is %v, want 0600 — copy-out imposed the 0644 default it guessed, loosening a private file to world-readable", got.Mode().Perm())
	}
	if body, rerr := os.ReadFile(hostSecret); rerr != nil || string(body) != "edited in the session\n" {
		t.Errorf("content = %q, %v — want the session's edit", body, rerr)
	}
}

// TestApplyOut_CleanSessionIsNoop pins that accepting a session which changed
// nothing writes nothing to the host.
//
// It used to assert a dedicated short-circuit: a captured state carrying a
// delta base but no patch meant "clean", so ApplyOut returned nil without
// consulting the diff — and a diffFailingSandbox wrapper proved the branch was
// really taken rather than quietly passing through a second no-op path. croft's
// move to containerd snapshot states removed that branch (see yaah-vlwm), so
// the property now holds the ordinary way: the diff against the baseline comes
// back empty and the copy-out loop has nothing to iterate.
//
// That is a weaker mechanism than the old short-circuit, so this test drives
// the real bootstrap rather than a canned state: an untouched session must
// leave the host byte-identical, which is the guarantee a user actually relies
// on when they accept work that turned out to change nothing.
func TestApplyOut_CleanSessionIsNoop(t *testing.T) {
	src := gitHostRepo(t)
	before, err := os.ReadFile(filepath.Join(src, "readme.txt"))
	if err != nil {
		t.Fatalf("read host readme.txt before the session: %v", err)
	}

	open, _ := cleanOpener()
	ss, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, src, nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	defer ss.Destroy(context.Background())

	// No writes through the workspace: the session is exactly as bootstrapped.
	// Assert the diff is genuinely empty first, so a later pass cannot be
	// "nothing changed" for the wrong reason (a diff that failed to see edits).
	diff, changed, err := ss.DiffSinceBaseline(context.Background(), 0)
	if err != nil {
		t.Fatalf("DiffSinceBaseline on an untouched session: %v", err)
	}
	if len(changed) != 0 || diff != "" {
		t.Fatalf("an untouched session must diff clean, got %d paths and diff %q", len(changed), diff)
	}

	if err := ss.ApplyOut(context.Background(), src); err != nil {
		t.Fatalf("ApplyOut on a clean session = %v, want nil", err)
	}

	after, err := os.ReadFile(filepath.Join(src, "readme.txt"))
	if err != nil {
		t.Fatalf("read host readme.txt after apply-out: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("a clean apply-out modified the host: readme.txt %q became %q", before, after)
	}
	if _, err := os.Lstat(filepath.Join(src, "result.txt")); !os.IsNotExist(err) {
		t.Errorf("a clean apply-out created result.txt on the host (%v)", err)
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

// The bootstrap's source checks: each layout below copies in without a
// self-contained repository, so each must fail the session at startup instead
// of handing the agent a tree whose repository data is missing.

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
// records them. The fake's shell has no git; the recorder is how a test proves
// the bootstrap runs no container-side git at all.
type gitRecordingSandbox struct {
	goatherdsandbox.Sandbox
	mu       sync.Mutex
	gitCalls []string
}

func (g *gitRecordingSandbox) Exec(ctx context.Context, req goatherdsandbox.ExecRequest) (goatherdsandbox.ExecResult, error) {
	if req.Command == "git" {
		g.mu.Lock()
		g.gitCalls = append(g.gitCalls, strings.Join(req.Args, " "))
		g.mu.Unlock()
		return goatherdsandbox.ExecResult{ExitCode: 0}, nil
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
func recordingOpener(rec goatherdsandbox.Sandbox) SandboxOpener {
	var opened int
	return func(config.WorkspaceConfig) (goatherdsandbox.Sandbox, error) {
		opened++
		if opened == 1 {
			sb := sandboxfake.New()
			sb.ExecHook = func(goatherdsandbox.ExecRequest) (goatherdsandbox.ExecResult, error) {
				return goatherdsandbox.ExecResult{ExitCode: 0}, nil
			}
			return sb, nil
		}
		return rec, nil
	}
}

// TestBootstrap_RunsNoContainerGit pins the snapshot-native bootstrap: even a
// dirty source is copied in and captured as-is. There is no synthetic commit,
// because the session's diffs and apply-out are measured against the captured
// snapshot, not against an in-container HEAD.
func TestBootstrap_RunsNoContainerGit(t *testing.T) {
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

	if calls := rec.calls(); len(calls) != 0 {
		t.Errorf("container git calls = %q, want none — the baseline is a snapshot state", calls)
	}
	if ss.Baseline() == nil {
		t.Error("the bootstrap must capture a baseline")
	}
}
