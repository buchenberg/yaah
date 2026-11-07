package dispatch

// proposal_eol_test.go pins the repo-policy half of PR #824 review finding 3:
// whether an EOL-only difference between the basis blob and the parent's
// worktree file is git's own smudge or a real parent edit is a question about
// the repo, not about the bytes. These tests read the answer from real git
// config and real .gitattributes files, so the probes cannot drift into
// answering "normalizes" for everything.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEOLNormalization_RepoPolicy(t *testing.T) {
	ctx := context.Background()

	t.Run("core.autocrlf=true normalizes every path", func(t *testing.T) {
		repo := newTestGitRepo(t)
		mustRunGit(t, repo, "config", "core.autocrlf", "true")

		eol := newEOLNormalization(ctx, repo)
		if !eol.autocrlf {
			t.Fatalf("autocrlf not detected in %q", repo)
		}
		for _, path := range []string{"README.md", "cmd/main.go", "nested/deep/notes.txt"} {
			if !eol.expected(path) {
				t.Errorf("autocrlf=true must normalize %q", path)
			}
		}
	})

	t.Run("core.autocrlf=input normalizes every path", func(t *testing.T) {
		repo := newTestGitRepo(t)
		mustRunGit(t, repo, "config", "core.autocrlf", "input")

		eol := newEOLNormalization(ctx, repo)
		if !eol.autocrlf {
			t.Fatal("autocrlf=input converts on commit and must count as normalization")
		}
		if !eol.expected("README.md") {
			t.Error("autocrlf=input must normalize README.md")
		}
	})

	t.Run(".gitattributes text rule normalizes only the matching path", func(t *testing.T) {
		repo := newTestGitRepo(t)
		mustRunGit(t, repo, "config", "core.autocrlf", "false")
		writeTestFile(t, repo, ".gitattributes", "*.txt text eol=crlf\n")

		eol := newEOLNormalization(ctx, repo)
		if eol.autocrlf {
			t.Fatal("autocrlf=false must not count as normalization")
		}
		if !eol.expected("notes.txt") {
			t.Error("a text/eol rule must normalize notes.txt")
		}
		if eol.expected("main.go") {
			t.Error("a *.txt rule must not normalize main.go")
		}
	})

	t.Run("text=auto normalizes text paths", func(t *testing.T) {
		repo := newTestGitRepo(t)
		mustRunGit(t, repo, "config", "core.autocrlf", "false")
		writeTestFile(t, repo, ".gitattributes", "* text=auto\n")

		if eol := newEOLNormalization(ctx, repo); !eol.expected("main.go") {
			t.Error("text=auto must normalize a text path")
		}
	})

	t.Run("-text opts a path out", func(t *testing.T) {
		repo := newTestGitRepo(t)
		mustRunGit(t, repo, "config", "core.autocrlf", "false")
		writeTestFile(t, repo, ".gitattributes", "*.bin -text\n")

		if eol := newEOLNormalization(ctx, repo); eol.expected("blob.bin") {
			t.Error("-text means the path keeps its bytes, so it is never expected to be smudged")
		}
	})

	t.Run("no policy leaves the path raw", func(t *testing.T) {
		repo := newTestGitRepo(t)
		mustRunGit(t, repo, "config", "core.autocrlf", "false")

		eol := newEOLNormalization(ctx, repo)
		if eol.autocrlf || eol.expected("README.md") {
			t.Error("a repo with no autocrlf and no attributes must not claim normalization")
		}
	})

	t.Run("a missing repo reads as no normalization", func(t *testing.T) {
		eol := newEOLNormalization(ctx, filepath.Join(t.TempDir(), "not-a-repo"))
		if eol.expected("README.md") {
			t.Error("an unreadable repo must classify an EOL-only difference as a parent edit")
		}
	})
}

func TestEOLNormalization_PathProbeIsCached(t *testing.T) {
	calls := 0
	eol := &eolNormalization{lookup: func(string) bool {
		calls++
		return true
	}}
	first := eol.expected("a.txt")
	again := eol.expected("a.txt")
	if !first || !again {
		t.Fatal("a stubbed rule must report normalization")
	}
	if calls != 1 {
		t.Errorf("probed the path %d times, want it cached after 1", calls)
	}
	eol.lookup = func(string) bool {
		calls++
		return false
	}
	if eol.expected("b.txt") || calls != 2 {
		t.Error("the cache must be per path, not one answer for the repo")
	}
}

// TestEOLNormalization_AutocrlfOverridesPathProbe pins the cost model: with
// autocrlf on, no per-path git call is needed at all.
func TestEOLNormalization_AutocrlfOverridesPathProbe(t *testing.T) {
	calls := 0
	eol := &eolNormalization{autocrlf: true, lookup: func(string) bool {
		calls++
		return false
	}}
	if !eol.expected("main.go") {
		t.Error("autocrlf must normalize every path")
	}
	if calls != 0 {
		t.Errorf("autocrlf answered for the path but the probe still ran %d time(s)", calls)
	}
}

// TestEOLNormalization_ZeroValueIsRaw guards the nil/zero paths the drift check
// relies on: an unavailable policy must never authorize a fold.
func TestEOLNormalization_ZeroValueIsRaw(t *testing.T) {
	var nilPolicy *eolNormalization
	if nilPolicy.expected("README.md") {
		t.Error("a nil policy must not claim normalization")
	}
	if (&eolNormalization{}).expected("README.md") {
		t.Error("the zero value must not claim normalization")
	}
}

// TestNewEOLNormalization_ReadsRepoOnce guards the "cheap" half of the fix: the
// repo-level question is answered at construction, so a settle with no EOL-only
// difference spawns no git process per path.
func TestNewEOLNormalization_ReadsRepoOnce(t *testing.T) {
	repo := newTestGitRepo(t)
	mustRunGit(t, repo, "config", "core.autocrlf", "true")

	eol := newEOLNormalization(context.Background(), repo)
	if !eol.autocrlf {
		t.Fatal("autocrlf=true not picked up at construction")
	}
	if _, err := os.Stat(filepath.Join(repo, "README.md")); err != nil {
		t.Fatalf("fixture repo unusable: %v", err)
	}
}
