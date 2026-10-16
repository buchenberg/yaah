package tools

// confinement_acceptance_test.go is the acceptance that gates flipping
// agents.subagent.containment to "auto" (Plan 7 Phase 3). The seam tests prove
// the pieces: an in-root write lands, an out-of-root write is refused, the
// redirect points at the scratch. None of them prove the thing the flip
// actually risks — that a REAL workload works inside the jail, in the layout the
// default lane really uses.
//
// That layout matters, and the first version of this test got it wrong. A
// retained dispatch does not work in a plain checkout: it works in a LINKED git
// worktree, whose index and object store live in the original repository's
// .git — outside the jail's writable root. A `git init` fixture has its .git
// inside the root and hid that entirely.
//
// So this builds a real main repo + linked worktree, runs a genuine `go build`
// there under paddock (fail closed), and asserts:
//
//  1. the build succeeds — the redirect covers what a real toolchain writes;
//  2. its cache landed in the worktree scratch, not the host cache;
//  3. the identity pointer works — `git config --get user.email` inside the jail
//     finds the global identity the moved HOME would otherwise hide;
//  4. read-only git works (status/log), which is most of what a sub-agent does;
//  5. KNOWN BLOCKER, pinned: a confined `git add` is refused by the kernel,
//     because the linked worktree's index is outside the sole writable root.
//     This is why the flip is still gated. The fix cannot be "also grant
//     <repo>/.git" — that hands a confined sub-agent write access to the host
//     repository's refs and objects, the exact thing retention prevents. When
//     the git-write story is settled, this leg must change, which is the point
//     of pinning it;
//  6. the boundary holds: an out-of-root write is refused by the kernel, and the
//     refusal is a denial rather than a launch failure.
//
// Linux + Landlock + Go + git required; skipped elsewhere.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/shepherd-runtime-go/sandbox/confinement"
	"github.com/buchenberg/shepherd-runtime-go/sandbox/landlock"
)

func TestConfinedBuild_Acceptance(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Landlock is Linux-only")
	}
	backend := landlock.Detect()
	if backend == nil {
		t.Skip("no Landlock on this host")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no Go toolchain on PATH")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}

	// A global git identity, exactly as a normal user configures one — and split
	// across BOTH files git reads (name in the legacy file, email in the XDG
	// one). HOME is moved by the redirect, so the only way either can be found
	// inside the jail is the redirect's shim including both. (Clear any inherited
	// GIT_CONFIG_GLOBAL first, so the resolution under test is the HOME one.)
	if prev, ok := os.LookupEnv("GIT_CONFIG_GLOBAL"); ok {
		_ = os.Unsetenv("GIT_CONFIG_GLOBAL")
		t.Cleanup(func() { _ = os.Setenv("GIT_CONFIG_GLOBAL", prev) })
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	fakeHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeHome, ".gitconfig"),
		[]byte("[user]\n\tname = Acceptance\n"), 0o600); err != nil {
		t.Fatalf("seed gitconfig: %v", err)
	}
	// The XDG default location, git's FIRST-read global file, which the legacy
	// ~/.gitconfig then overrides per key.
	xdgDir := filepath.Join(fakeHome, ".config", "git")
	if err := os.MkdirAll(xdgDir, 0o755); err != nil {
		t.Fatalf("seed xdg dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdgDir, "config"),
		[]byte("[user]\n\temail = acceptance@example.com\n"), 0o600); err != nil {
		t.Fatalf("seed xdg config: %v", err)
	}
	t.Setenv("HOME", fakeHome)

	// The real retained layout: a main repository plus a LINKED worktree. The
	// worktree's git dir is <repo>/.git/worktrees/<name> and its object store is
	// <repo>/.git/objects — both outside the jail's writable root.
	repo := filepath.Join(t.TempDir(), "repo")
	worktree := filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	hostGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, gerr := cmd.CombinedOutput(); gerr != nil {
			t.Fatalf("git %v: %v\n%s", args, gerr, out)
		}
	}
	hostGit(repo, "init", "-q", "-b", "main", ".")
	hostGit(repo, "-c", "user.email=seed@example.com", "-c", "user.name=Seed", "commit", "-q", "--allow-empty", "-m", "seed")
	hostGit(repo, "worktree", "add", "-q", "-b", "retained", worktree, "main")

	// A dependency-free module in the worktree, and a go directive old enough
	// that no toolchain download is attempted: a download would need a write to
	// the module cache, which the single-writable-root jail refuses by design.
	write := func(rel, body string) {
		t.Helper()
		if werr := os.WriteFile(filepath.Join(worktree, rel), []byte(body), 0o644); werr != nil {
			t.Fatalf("seed %s: %v", rel, werr)
		}
	}
	write("go.mod", "module confined-acceptance\n\ngo 1.21\n")
	write("main.go", "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"confined\") }\n")

	// The lane's redirect, exactly as the runner attaches it (Plan 7 Phase 3).
	env, err := ConfinementScratchEnv(worktree)
	if err != nil {
		t.Fatalf("build confinement scratch: %v", err)
	}
	ws := confinedTestWorkspace(worktree, backend, confinement.PlacementPaddock).WithEnv(env)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// exec runs one confined command in the worktree.
	exec := func(command string, args ...string) (ExecResult, error) {
		t.Helper()
		return ws.Exec(ctx, ExecRequest{
			Command: command, Args: args, Cwd: worktree, SeparateStreams: true,
		})
	}
	run := func(why string, command string, args ...string) {
		t.Helper()
		res, rerr := exec(command, args...)
		if rerr != nil || res.ExitCode != 0 {
			t.Fatalf("%s: exit=%d err=%v\nstderr: %s", why, res.ExitCode, rerr, res.Stderr)
		}
	}

	// 1. A real build runs to completion inside the jail.
	run("a real build must succeed inside the jail", goBin, "build", "./...")

	// 2. The redirect was in force: the build cache is in the worktree scratch,
	// not the host's GOCACHE. (An empty cache would mean the build read the
	// host's and this assertion could not distinguish the two, so require
	// content.)
	cache := filepath.Join(worktree, confinementScratchName, "go-build")
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatalf("the redirect must have placed the build cache in the worktree scratch: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the scratch build cache is empty — the build did not use the redirect")
	}

	// 3. Both halves of the identity are visible inside the jail: the redirect's
	// shim must merge BOTH real files, not just one.
	for _, want := range []struct{ key, value string }{
		{"user.name", "Acceptance"},
		{"user.email", "acceptance@example.com"},
	} {
		res, rerr := exec("git", "config", "--get", want.key)
		if rerr != nil || res.ExitCode != 0 {
			t.Fatalf("the redirect must expose %s: exit=%d err=%v stderr=%s",
				want.key, res.ExitCode, rerr, res.Stderr)
		}
		if got := strings.TrimSpace(res.Stdout); got != want.value {
			t.Fatalf("%s inside the jail = %q, want %q (each lives in a different global file)",
				want.key, got, want.value)
		}
	}

	// 4. Read-only git works in the linked worktree — most of what a sub-agent
	// does with git (status, diff, log) needs no write.
	run("read-only git must work in a linked worktree", "git", "status", "--porcelain")

	// 5. KNOWN BLOCKER (see the file header): the retained lane's git metadata
	// is outside the writable root, so an index write is refused. Pinned here so
	// the flip cannot land without settling it — and so this leg fails loudly
	// when it is settled, rather than silently going stale.
	res, rerr := exec("git", "add", "-A")
	if res.ExitCode != 0 && !strings.Contains(res.Stderr, "Permission denied") {
		t.Fatalf("a denied index write must be a kernel denial, not a launch failure: exit=%d err=%v stderr=%q",
			res.ExitCode, rerr, res.Stderr)
	}
	if res.ExitCode == 0 {
		t.Fatalf("the linked-worktree index write is no longer refused — the git-write blocker "+
			"appears settled; update this leg (and the containment default) rather than leaving it stale: %+v", res)
	}

	// 6. The boundary survived the whole run: an out-of-root write is still
	// refused — by the kernel, not by a launch failure. (A jail that never
	// established would also "not write" the file, so assert the command RAN and
	// was denied, otherwise this leg passes vacuously. A launch failure is
	// ExitCode -1.)
	outside := filepath.Join(t.TempDir(), "escaped.txt")
	shell, flag := ws.Shell()
	res, rerr = exec(shell, flag, "echo no > "+outside)
	if res.ExitCode == -1 {
		t.Fatalf("the jail must establish, so the refusal is a denial and not a launch failure: %v", rerr)
	}
	if res.ExitCode == 0 {
		t.Fatalf("the boundary must hold; %s was written", outside)
	}
	if !strings.Contains(res.Stderr, "Permission denied") {
		t.Fatalf("the refusal must be a kernel denial, got exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
		t.Fatalf("the out-of-root file must not exist (stat err = %v)", statErr)
	}
}
