package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/buchenberg/goatherd-runtime/sandbox/containment"
	"github.com/buchenberg/goatherd-runtime/sandbox/landlock"
	"github.com/buchenberg/yaah/internal/config"
)

func TestResolveGrants(t *testing.T) {
	world := t.TempDir()

	t.Run("empty keeps the whole world writable", func(t *testing.T) {
		g, err := ResolveGrants(nil, world)
		if err != nil {
			t.Fatal(err)
		}
		if g.Restrictive() || len(g.Writable) != 1 || g.Writable[0] != world {
			t.Fatalf("got %+v, want the legacy single root", g)
		}
	})

	t.Run("readwrite and readonly lower to disjoint roots", func(t *testing.T) {
		g, err := ResolveGrants([]config.GrantConfig{
			{Path: "src", Access: "readwrite"},
			{Path: "docs", Access: "readonly"},
		}, world)
		if err != nil {
			t.Fatal(err)
		}
		if !g.Restrictive() || len(g.Writable) != 1 || len(g.ReadOnly) != 1 {
			t.Fatalf("got %+v", g)
		}
		if g.Writable[0] != filepath.Join(world, "src") || g.ReadOnly[0] != filepath.Join(world, "docs") {
			t.Fatalf("roots are not world-relative: %+v", g)
		}
	})

	t.Run("escaping the world is refused", func(t *testing.T) {
		if _, err := ResolveGrants([]config.GrantConfig{{Path: "../outside", Access: "readwrite"}}, world); err == nil {
			t.Fatal("an escaping grant must be refused")
		}
	})
}

// TestNarrowedGrants_EnforceSubRoots pins the authority difference on Linux:
// a readwrite grant is writable, an undeclared sibling inside the world is not.
func TestNarrowedGrants_EnforceSubRoots(t *testing.T) {
	backend := landlock.Detect()
	if backend == nil {
		t.Skip("no Landlock on this host")
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell redirection")
	}
	world := t.TempDir()
	src := filepath.Join(world, "src")
	docs := filepath.Join(world, "docs")
	for _, d := range []string{src, docs} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	ws := confinedTestWorkspace(world, backend, containment.PlacementPaddock).
		WithWritableRoots([]string{src})

	if res, err := ws.Exec(context.Background(), ExecRequest{
		Command: "sh", Args: []string{"-c", "echo ok > " + filepath.Join(src, "a.txt")},
	}); err != nil || res.ExitCode != 0 {
		t.Fatalf("a write under the readwrite grant must succeed: %+v err=%v", res, err)
	}

	res, err := ws.Exec(context.Background(), ExecRequest{
		Command: "sh", Args: []string{"-c", "echo no > " + filepath.Join(docs, "b.txt")},
	})
	if err == nil && res.ExitCode == 0 {
		t.Fatalf("an undeclared path inside the world must be refused: %+v", res)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "may write only under") {
		t.Errorf("a narrowed refusal must name the writable roots, got %+v", res)
	}
	if _, statErr := os.Stat(filepath.Join(docs, "b.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("the undeclared write must not land (stat err = %v)", statErr)
	}
}

// TestReadOnlyGrants_DenyAllWrites pins the all-readonly case: a declared set
// with no writable root means "nothing writable", not the legacy whole world.
// The housekeeping scratch is still admitted so redirected HOME/TMPDIR writes
// do not fail.
func TestReadOnlyGrants_DenyAllWrites(t *testing.T) {
	backend := landlock.Detect()
	if backend == nil {
		t.Skip("no Landlock on this host")
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell redirection")
	}
	world := t.TempDir()
	scratch := filepath.Join(world, ".yaah-scratch")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	ws := confinedTestWorkspace(world, backend, containment.PlacementPaddock).
		WithWritableRoots(nil)

	res, err := ws.Exec(context.Background(), ExecRequest{
		Command: "sh", Args: []string{"-c", "echo no > " + filepath.Join(world, "a.txt")},
	})
	if err == nil && res.ExitCode == 0 {
		t.Fatalf("an all-readonly dispatch must deny writes in the world: %+v", res)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "no writable root") {
		t.Errorf("the refusal must explain the read-only grant set, got %+v", res)
	}

	if res, err := ws.Exec(context.Background(), ExecRequest{
		Command: "sh", Args: []string{"-c", "echo ok > " + filepath.Join(scratch, "tmp.txt")},
	}); err != nil || res.ExitCode != 0 {
		t.Fatalf("the scratch must stay writable: %+v err=%v", res, err)
	}
}
