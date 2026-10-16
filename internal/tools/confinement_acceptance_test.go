package tools

// confinement_acceptance_test.go is the acceptance that gates flipping
// agents.subagent.containment to "auto" (Plan 7 Phase 3). Phase 1/3's unit
// tests prove the seam: an in-root write lands, an out-of-root write is
// refused, and the redirect points at the scratch. None of them prove the
// thing the default flip actually risks — that a REAL toolchain works inside
// the jail, with only one writable root and every housekeeping write and
// lookup redirected into the worktree.
//
// So this runs a genuine `go build` and a genuine `git commit` under paddock
// (fail closed: no silent advisory fallback), then asserts:
//
//  1. the build succeeds — the redirect covers what a real toolchain writes;
//  2. its cache landed in the worktree scratch, not the host cache — the
//     redirect was in force, not coincidentally unnecessary;
//  3. the commit succeeds with an identity that exists only in a global
//     ~/.gitconfig — the redirect must not hide the one READ a commit needs;
//  4. an out-of-root write is still refused — the boundary survived both.
//
// The git leg is the one the redirect-as-shipped failed: HOME moves, git finds
// no global identity, and the commit dies with "Author identity unknown". The
// fixture supplies the identity the way a real user does (a global file) rather
// than with repo-local config, so the carry is what makes it pass.
//
// Linux + Landlock + Go + git required; skipped elsewhere. See
// docs/shepherd-benchmark.md for the wider benchmark this is the cheap proxy
// for.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

	// A global git identity, exactly as a normal user configures one. HOME is
	// moved by the redirect, so a repo with no local identity can only commit if
	// the redirect carries this file.
	fakeHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeHome, ".gitconfig"),
		[]byte("[user]\n\tname = Acceptance\n\temail = acceptance@example.com\n"), 0o600); err != nil {
		t.Fatalf("seed gitconfig: %v", err)
	}
	t.Setenv("HOME", fakeHome)

	root := t.TempDir()
	// A dependency-free module, and a go directive old enough that no toolchain
	// download is attempted: a download would need a write to the module cache,
	// which the single-writable-root jail refuses by design.
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatalf("seed %s: %v", rel, err)
		}
	}
	write("go.mod", "module confined-acceptance\n\ngo 1.21\n")
	write("main.go", "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"confined\") }\n")

	// The lane's redirect, exactly as the runner attaches it (Plan 7 Phase 3).
	env, err := ConfinementScratchEnv(root)
	if err != nil {
		t.Fatalf("build confinement scratch: %v", err)
	}
	ws := confinedTestWorkspace(root, backend, confinement.PlacementPaddock).WithEnv(env)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// run executes one confined command, failing on a non-zero exit.
	run := func(why string, command string, args ...string) {
		t.Helper()
		res, err := ws.Exec(ctx, ExecRequest{
			Command: command, Args: args, Cwd: root, SeparateStreams: true,
		})
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("%s: exit=%d err=%v\nstderr: %s", why, res.ExitCode, err, res.Stderr)
		}
	}

	// 1. A real build runs to completion inside the jail.
	run("a real build must succeed inside the jail", goBin, "build", "./...")

	// 2. The redirect was in force: the build cache is in the worktree scratch,
	// not the host's GOCACHE. (An empty cache would mean the build read the
	// host's and this assertion could not distinguish the two, so require
	// content.)
	cache := filepath.Join(root, confinementScratchName, "go-build")
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatalf("the redirect must have placed the build cache in the worktree scratch: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the scratch build cache is empty — the build did not use the redirect")
	}

	// 3. A commit finds the identity the redirect carried. Without the carry git
	// reads an empty scratch HOME and refuses ("Author identity unknown").
	run("git init in the worktree", "git", "init", "-q", ".")
	run("stage", "git", "add", "-A")
	run("a confined git commit must find the carried global identity", "git", "commit", "-q", "-m", "confined commit")

	// 4. The boundary survived both: an out-of-root write is still refused.
	outside := filepath.Join(t.TempDir(), "escaped.txt")
	shell, flag := ws.Shell()
	if r, _ := ws.Exec(context.Background(), ExecRequest{
		Command: shell, Args: []string{flag, "echo no > " + outside},
	}); r.ExitCode == 0 {
		t.Fatalf("the boundary must hold after a real build; %s was written", outside)
	}
	if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
		t.Fatalf("the out-of-root file must not exist (stat err = %v)", statErr)
	}
}
