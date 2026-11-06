package tools

// gitHostRepo_test.go duplicates the git-host fixture that lives in
// internal/tools/substrate's tests: test helpers cannot cross package
// boundaries, and both tools and substrate bootstrap tests need a committed
// git source. Keep the two copies in sync.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
	// content to CRLF at apply time, and byte-exact assertions would then
	// compare against the runner's line-ending policy instead of the patch.
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
