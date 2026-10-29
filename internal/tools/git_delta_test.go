package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/sandboxfake"
)

// git_delta_test.go covers the container settlement artifact without a
// containerd daemon: the raw-diff parser, the host writer, the persistent
// proposal lifecycle, and one end-to-end dispatch over a fake sandbox whose
// Exec is a canned git responder. The fake still exercises the real proposal
// plumbing — bootstrap copy-in, base capture, seal, persist, destroy, settle —
// only git itself is canned.

func TestParseGitRawDiff(t *testing.T) {
	raw := ":000000 100644 0000000 1111111 A\x00dir/new file.txt\x00" +
		":100644 100755 2222222 3333333 M\x00bin/run.sh\x00" +
		":100644 000000 4444444 0000000 D\x00gone\nname.txt\x00"

	entries, err := parseGitRawDiff(raw)
	if err != nil {
		t.Fatalf("parseGitRawDiff: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	if got, want := entries[0], (rawDiffEntry{OldMode: "000000", NewMode: "100644", OldSHA: "0000000", NewSHA: "1111111", Status: "A", Path: "dir/new file.txt"}); got != want {
		t.Errorf("add entry = %+v, want %+v", got, want)
	}
	if entries[1].NewMode != "100755" || entries[1].Status != "M" {
		t.Errorf("modify entry = %+v, want mode 100755 and status M", entries[1])
	}
	if entries[2].Status != "D" || entries[2].Path != "gone\nname.txt" {
		t.Errorf("delete entry = %+v, want a NUL-safe path with an embedded newline", entries[2])
	}

	t.Run("rename records are refused", func(t *testing.T) {
		_, err := parseGitRawDiff(":100644 100644 a b R100\x00new\x00old\x00")
		if err == nil || !strings.Contains(err.Error(), "rename") {
			t.Fatalf("error = %v, want a rename refusal", err)
		}
	})

	t.Run("malformed records are refused", func(t *testing.T) {
		for _, raw := range []string{
			"not-a-header\x00",
			":100644 100644 a b\x00path\x00",
			":100644 100644 a b M\x00",
		} {
			if _, err := parseGitRawDiff(raw); err == nil {
				t.Errorf("parseGitRawDiff(%q) must fail", raw)
			}
		}
	})
}

func TestGitChangeKind(t *testing.T) {
	for status, want := range map[string]string{"A": deltaKindAdd, "M": deltaKindModify, "D": deltaKindDelete} {
		got, err := gitChangeKind(status)
		if err != nil || got != want {
			t.Errorf("gitChangeKind(%q) = %q, %v, want %q", status, got, err, want)
		}
	}
	for _, status := range []string{"", "T", "U", "X"} {
		if _, err := gitChangeKind(status); err == nil {
			t.Errorf("gitChangeKind(%q) must be refused", status)
		}
	}
}

func TestParseGitMode(t *testing.T) {
	for mode, want := range map[string]fs.FileMode{"100644": 0o644, "100755": 0o755} {
		got, err := parseGitMode(mode)
		if err != nil || got != want {
			t.Errorf("parseGitMode(%q) = %v, %v, want %v", mode, got, err, want)
		}
	}
	for _, mode := range []string{"120000", "160000", "100600", ""} {
		if _, err := parseGitMode(mode); err == nil {
			t.Errorf("parseGitMode(%q) must be refused", mode)
		}
	}
}

func TestSafeDeltaPath(t *testing.T) {
	valid := map[string]string{
		"a/b.txt": "a/b.txt",
		"a/./b":   "a/b",
		"a//b":    "a/b",
	}
	for in, want := range valid {
		got, err := safeDeltaPath(in)
		if err != nil || got != want {
			t.Errorf("safeDeltaPath(%q) = %q, %v, want %q", in, got, err, want)
		}
	}

	invalid := []string{
		"",
		"/etc/passwd",
		"../escape",
		"a/../../b",
		".git/config",
		"a/.git/config",
		`a\b`,
		"C:/windows",
		"a\x00b",
	}
	for _, in := range invalid {
		if got, err := safeDeltaPath(in); err == nil {
			t.Errorf("safeDeltaPath(%q) = %q, want an error", in, got)
		}
	}
}

func TestValidateDelta(t *testing.T) {
	base := &GitDelta{Changes: []DeltaChange{{Path: "a.txt", Kind: deltaKindAdd, Mode: 0o644, Content: []byte("a")}}}
	if err := validateDelta(base); err != nil {
		t.Fatalf("valid delta refused: %v", err)
	}

	cases := map[string]*GitDelta{
		"duplicate paths": {Changes: []DeltaChange{
			{Path: "a.txt", Kind: deltaKindAdd, Mode: 0o644},
			{Path: "a.txt", Kind: deltaKindModify, Mode: 0o644},
		}},
		"unknown kind":        {Changes: []DeltaChange{{Path: "a.txt", Kind: "rename", Mode: 0o644}}},
		"delete with content": {Changes: []DeltaChange{{Path: "a.txt", Kind: deltaKindDelete, Content: []byte("x")}}},
		"non-regular mode":    {Changes: []DeltaChange{{Path: "a.txt", Kind: deltaKindAdd, Mode: 0o600}}},
		"unnormalized path":   {Changes: []DeltaChange{{Path: "a/./b", Kind: deltaKindAdd, Mode: 0o644}}},
		"unsafe path":         {Changes: []DeltaChange{{Path: "../a", Kind: deltaKindAdd, Mode: 0o644}}},
	}
	for name, delta := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateDelta(delta); err == nil {
				t.Fatalf("%s must be refused", name)
			}
		})
	}

	t.Run("content cap", func(t *testing.T) {
		big := &GitDelta{Changes: []DeltaChange{{
			Path: "big.bin", Kind: deltaKindAdd, Mode: 0o644,
			Content: make([]byte, gitDeltaMaxContentBytes+1),
		}}}
		if err := validateDelta(big); err == nil || !strings.Contains(err.Error(), "cap") {
			t.Fatalf("error = %v, want the content cap", err)
		}
	})

	t.Run("file count cap", func(t *testing.T) {
		changes := make([]DeltaChange, gitDeltaMaxFiles+1)
		for i := range changes {
			changes[i] = DeltaChange{Path: "f" + strconv.Itoa(i), Kind: deltaKindDelete}
		}
		if err := validateDelta(&GitDelta{Changes: changes}); err == nil || !strings.Contains(err.Error(), "file cap") {
			t.Fatalf("error = %v, want the file-count cap", err)
		}
	})
}

func TestDecodeBase64Blob(t *testing.T) {
	want := []byte{0x00, 0x01, 0xff, 'h', 'i', '\n'}
	encoded := base64.StdEncoding.EncodeToString(want)
	// base64(1) wraps at 76 columns; simulate that and stray indentation.
	withSpace := "  " + encoded[:4] + "\n" + encoded[4:] + "\t\n"
	got, err := decodeBase64Blob(withSpace)
	if err != nil {
		t.Fatalf("decodeBase64Blob: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("decoded = %v, want %v", got, want)
	}
	if _, err := decodeBase64Blob("not base64!"); err == nil {
		t.Error("invalid base64 must fail")
	}
}

func TestGitDeltaRenderDiff(t *testing.T) {
	delta := &GitDelta{Changes: []DeltaChange{
		{Path: "added.txt", Kind: deltaKindAdd, Mode: 0o644, Content: []byte("hello\n")},
		{Path: "changed.txt", Kind: deltaKindModify, Mode: 0o755, Content: []byte("world\n")},
		{Path: "gone.txt", Kind: deltaKindDelete, Mode: 0o644},
		{Path: "raw.bin", Kind: deltaKindAdd, Mode: 0o644, Content: []byte{0x00, 0xff}},
	}}
	got := delta.RenderDiff()
	for _, want := range []string{
		"--- /dev/null\n+++ b/added.txt",
		"+hello",
		"--- a/changed.txt\n+++ b/changed.txt",
		"+world",
		"--- a/gone.txt\n+++ /dev/null",
		"Binary file raw.bin",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderDiff missing %q in:\n%s", want, got)
		}
	}
}

func TestApplyGitDelta(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "keep.txt", "keep\n")
	writeTestFile(t, root, "change.txt", "before\n")
	writeTestFile(t, root, "gone.txt", "remove me\n")

	delta := &GitDelta{Changes: []DeltaChange{
		{Path: "dir/added.sh", Kind: deltaKindAdd, Mode: 0o755, Content: []byte("#!/bin/sh\n")},
		{Path: "change.txt", Kind: deltaKindModify, Mode: 0o755, Content: []byte("after\n")},
		{Path: "gone.txt", Kind: deltaKindDelete, Mode: 0o644},
	}}
	if err := applyGitDelta(context.Background(), root, delta); err != nil {
		t.Fatalf("applyGitDelta: %v", err)
	}

	if got, err := os.ReadFile(filepath.Join(root, "dir/added.sh")); err != nil || string(got) != "#!/bin/sh\n" {
		t.Errorf("added file = %q, %v", got, err)
	}
	if info, err := os.Stat(filepath.Join(root, "dir/added.sh")); err != nil {
		t.Errorf("added file stat: %v", err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		// Windows has no Unix mode bits; chmod is a no-op and Go reports 0666.
		t.Errorf("added mode = %v, want 0755", info.Mode().Perm())
	}
	if got, err := os.ReadFile(filepath.Join(root, "change.txt")); err != nil || string(got) != "after\n" {
		t.Errorf("modified file = %q, %v", got, err)
	}
	if info, err := os.Stat(filepath.Join(root, "change.txt")); err != nil {
		t.Errorf("modified file stat: %v", err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		t.Errorf("modified mode = %v, want 0755", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(root, "gone.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("deleted file still present (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(root, "keep.txt")); err != nil {
		t.Errorf("untouched file changed: %v", err)
	}
}

func TestApplyGitDelta_RefusesUnsafePaths(t *testing.T) {
	for _, p := range []string{"../escape.txt", ".git/config", "/etc/evil"} {
		root := t.TempDir()
		delta := &GitDelta{Changes: []DeltaChange{{Path: p, Kind: deltaKindAdd, Mode: 0o644, Content: []byte("x")}}}
		if err := applyGitDelta(context.Background(), root, delta); err == nil {
			t.Errorf("path %q must be refused", p)
		}
		if entries, _ := os.ReadDir(root); len(entries) != 0 {
			t.Errorf("path %q wrote something into the root: %v", p, entries)
		}
	}
}

func TestApplyGitDelta_RefusesSymlinkedParentEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	delta := &GitDelta{Changes: []DeltaChange{
		{Path: "link/escape.txt", Kind: deltaKindAdd, Mode: 0o644, Content: []byte("x")},
	}}
	if err := applyGitDelta(context.Background(), root, delta); err == nil {
		t.Fatal("a write through a symlinked parent outside the repo must be refused")
	}
	if _, err := os.Stat(filepath.Join(outside, "escape.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the write escaped the repository (err=%v)", err)
	}

	writeTestFile(t, outside, "victim.txt", "outside\n")
	del := &GitDelta{Changes: []DeltaChange{{Path: "link/victim.txt", Kind: deltaKindDelete}}}
	if err := applyGitDelta(context.Background(), root, del); err == nil {
		t.Fatal("a delete through a symlinked parent outside the repo must be refused")
	}
	if _, err := os.Stat(filepath.Join(outside, "victim.txt")); err != nil {
		t.Errorf("the delete escaped the repository: %v", err)
	}
}

func TestHostChangedPaths(t *testing.T) {
	repo := newTestGitRepo(t)
	writeTestFile(t, repo, "modified.txt", "before\n")
	writeTestFile(t, repo, "deleted.txt", "before\n")
	mustRunGit(t, repo, "add", "-A")
	mustRunGit(t, repo, "commit", "-m", "seed")

	writeTestFile(t, repo, "modified.txt", "after\n")
	if err := os.Remove(filepath.Join(repo, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, repo, "untracked.txt", "new\n")

	paths, err := hostChangedPaths(context.Background(), repo)
	if err != nil {
		t.Fatalf("hostChangedPaths: %v", err)
	}
	set := make(map[string]bool)
	for _, p := range paths {
		set[p] = true
	}
	for _, want := range []string{"modified.txt", "deleted.txt", "untracked.txt"} {
		if !set[want] {
			t.Errorf("host changes %v missing %q", paths, want)
		}
	}

	if _, err := hostChangedPaths(context.Background(), t.TempDir()); err == nil {
		t.Error("a non-repository directory must fail closed")
	}
}

// deltaFixture is one real repository with a root scope, the pieces a container
// proposal's settlement needs.
type deltaFixture struct {
	fx          *sessionFixtures
	repo        string
	rootID      string
	basisState  shepherdtrace.WorkspaceState
	basisDigest string
	sessionID   string
	registry    *ProposalRegistry
}

func newDeltaFixture(t *testing.T) *deltaFixture {
	return newDeltaFixtureWith(t, nil)
}

// newDeltaFixtureWith lets a test dirty the host before the basis is captured,
// modelling a dispatch from an already-dirty tree.
func newDeltaFixtureWith(t *testing.T, prep func(repo string)) *deltaFixture {
	t.Helper()
	fx, repo, rootID := newSessionFixtures(t)
	if prep != nil {
		prep(repo)
	}
	parent, ok := fx.mgr.Get(rootID)
	if !ok {
		t.Fatalf("root scope %s not found", rootID)
	}
	state, err := parent.CaptureWorkspace(context.Background())
	if err != nil {
		t.Fatalf("capture host basis: %v", err)
	}
	digest, err := state.Digest()
	if err != nil {
		t.Fatalf("digest host basis: %v", err)
	}
	return &deltaFixture{fx: fx, repo: repo, rootID: rootID, basisState: state, basisDigest: digest, sessionID: "test-session", registry: NewProposalRegistry()}
}

// add persists a delta and registers it as an open proposal.
func (f *deltaFixture) add(t *testing.T, id string, delta *GitDelta) *retainedProposal {
	t.Helper()
	dir, digest, err := persistGitDelta(context.Background(), f.repo, id, delta, "review for "+id, "wrote "+id, "", f.sessionID, f.rootID, "host", f.basisState, f.basisDigest)
	if err != nil {
		t.Fatalf("persistGitDelta: %v", err)
	}
	p := &retainedProposal{
		id:          id,
		delta:       delta,
		result:      "wrote " + id,
		parentID:    f.rootID,
		mgr:         f.fx.mgr,
		repoPath:    f.repo,
		basisState:  f.basisState,
		basisDigest: f.basisDigest,
		artifactDir: dir,
		deltaDigest: digest,
		world:       "container",
		target:      "host",
	}
	f.registry.add(p)
	return p
}

func (f *deltaFixture) settle(t *testing.T, id, action string) proposalEnvelope {
	t.Helper()
	out, err := f.registry.settleRetainedProposal(context.Background(), id, action)
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	return decodeProposalEnvelope(t, out)
}

func TestGitDeltaSettle_SelectLandsAndConsumes(t *testing.T) {
	f := newDeltaFixture(t)
	delta := &GitDelta{Changes: []DeltaChange{
		{Path: "out.txt", Kind: deltaKindAdd, Mode: 0o644, Content: []byte("landed\n")},
		{Path: "README.md", Kind: deltaKindModify, Mode: 0o644, Content: []byte("# Changed\n")},
	}}
	p := f.add(t, "proposal:select", delta)

	// The manifest must be invisible to git status, or select's own basis
	// comparison would fail the moment the seal wrote it.
	changed, err := hostChangedPaths(context.Background(), f.repo)
	if err != nil {
		t.Fatalf("hostChangedPaths: %v", err)
	}
	for _, c := range changed {
		if strings.HasPrefix(c, ".yaah/") {
			t.Errorf("proposal bookkeeping leaked into git status: %q", c)
		}
	}

	env := f.settle(t, p.id, "select")
	if env.Status != "settled" || env.Action != "select" || env.World != "container" {
		t.Fatalf("select envelope = %+v", env)
	}
	if got, err := os.ReadFile(filepath.Join(f.repo, "out.txt")); err != nil || string(got) != "landed\n" {
		t.Errorf("out.txt = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(f.repo, "README.md")); err != nil || string(got) != "# Changed\n" {
		t.Errorf("README.md = %q, %v", got, err)
	}
	if _, err := os.Stat(p.artifactDir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the proposal directory must be consumed with the settlement (err=%v)", err)
	}
	if _, err := f.registry.get(p.id); err == nil {
		t.Error("a settled proposal must leave the registry")
	}
	if _, err := f.registry.settleRetainedProposal(context.Background(), p.id, "discard"); err == nil {
		t.Error("a second settlement must be refused")
	}
}

func TestGitDeltaSettle_SelectRefusesMovedHost(t *testing.T) {
	f := newDeltaFixture(t)
	delta := &GitDelta{Changes: []DeltaChange{
		{Path: "out.txt", Kind: deltaKindAdd, Mode: 0o644, Content: []byte("landed\n")},
	}}
	p := f.add(t, "proposal:moved", delta)

	// The host moves after the basis was captured.
	writeTestFile(t, f.repo, "README.md", "# Moved on the host\n")

	if _, err := f.registry.settleRetainedProposal(context.Background(), p.id, "select"); err == nil {
		t.Fatal("select must fail closed when the host moved")
	} else if !strings.Contains(err.Error(), "moved") {
		t.Errorf("error = %v, want it to say the host moved", err)
	}
	if _, err := os.Stat(filepath.Join(f.repo, "out.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused select must write nothing (err=%v)", err)
	}
	if _, err := f.registry.get(p.id); err != nil {
		t.Errorf("a refused select must leave the proposal open: %v", err)
	}
}

func TestGitDeltaSettle_ApplyRefusesOverlap(t *testing.T) {
	f := newDeltaFixture(t)
	delta := &GitDelta{Changes: []DeltaChange{
		{Path: "README.md", Kind: deltaKindModify, Mode: 0o644, Content: []byte("# from the container\n")},
	}}
	p := f.add(t, "proposal:overlap", delta)

	writeTestFile(t, f.repo, "README.md", "# the host's version\n")

	if _, err := f.registry.settleRetainedProposal(context.Background(), p.id, "apply"); err == nil {
		t.Fatal("apply must refuse an overlapping path")
	} else if !strings.Contains(err.Error(), "path-disjoint") || !strings.Contains(err.Error(), "README.md") {
		t.Errorf("error = %v, want a path-disjoint refusal naming the path", err)
	}
	if got, _ := os.ReadFile(filepath.Join(f.repo, "README.md")); string(got) != "# the host's version\n" {
		t.Errorf("apply must write nothing on overlap, README.md = %q", got)
	}
	if _, err := f.registry.get(p.id); err != nil {
		t.Errorf("a refused apply must leave the proposal open: %v", err)
	}
}

func TestGitDeltaSettle_ApplyWritesDisjointPaths(t *testing.T) {
	f := newDeltaFixture(t)
	delta := &GitDelta{Changes: []DeltaChange{
		{Path: "dir/out.txt", Kind: deltaKindAdd, Mode: 0o644, Content: []byte("landed\n")},
	}}
	p := f.add(t, "proposal:disjoint", delta)

	writeTestFile(t, f.repo, "README.md", "# the host's version\n")

	env := f.settle(t, p.id, "apply")
	if env.Action != "apply" || env.Status != "settled" {
		t.Fatalf("apply envelope = %+v", env)
	}
	if got, err := os.ReadFile(filepath.Join(f.repo, "dir/out.txt")); err != nil || string(got) != "landed\n" {
		t.Errorf("dir/out.txt = %q, %v", got, err)
	}
	if got, _ := os.ReadFile(filepath.Join(f.repo, "README.md")); string(got) != "# the host's version\n" {
		t.Errorf("apply must leave the host's own change alone, README.md = %q", got)
	}
}

func TestGitDeltaSettle_ReleaseExportsThenConsumes(t *testing.T) {
	f := newDeltaFixture(t)
	delta := &GitDelta{Changes: []DeltaChange{
		{Path: "out.txt", Kind: deltaKindAdd, Mode: 0o644, Content: []byte("kept\n")},
	}}
	p := f.add(t, "proposal:release", delta)

	env := f.settle(t, p.id, "release")
	if env.Retained == "" {
		t.Fatalf("release envelope must carry the retention path: %+v", env)
	}
	data, err := os.ReadFile(filepath.Join(env.Retained, "delta.json"))
	if err != nil {
		t.Fatalf("retained delta not readable: %v", err)
	}
	if !strings.Contains(string(data), "out.txt") {
		t.Errorf("retained delta does not name the changed path:\n%s", data)
	}
	if _, err := os.Stat(p.artifactDir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("release must consume the proposal directory (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(f.repo, "out.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("release must not write the host (err=%v)", err)
	}
}

func TestGitDeltaSettle_DiscardRemovesProposal(t *testing.T) {
	f := newDeltaFixture(t)
	delta := &GitDelta{Changes: []DeltaChange{
		{Path: "out.txt", Kind: deltaKindAdd, Mode: 0o644, Content: []byte("thrown away\n")},
	}}
	p := f.add(t, "proposal:discard", delta)

	env := f.settle(t, p.id, "discard")
	if env.Action != "discard" || env.Status != "settled" {
		t.Fatalf("discard envelope = %+v", env)
	}
	if _, err := os.Stat(p.artifactDir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("discard must remove the proposal directory (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(f.repo, "out.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("discard must not write the host (err=%v)", err)
	}
	if _, err := f.registry.get(p.id); err == nil {
		t.Error("discard must consume the proposal")
	}
}

// TestCaptureGitDelta_RefusesOversizedAndUnsafe covers the producer's fail
// paths: a blob above the cap is refused before it is transferred, and an
// unsafe path from git is refused rather than applied.
func TestCaptureGitDelta_RefusesOversizedAndUnsafe(t *testing.T) {
	t.Run("oversized blob", func(t *testing.T) {
		sb := sandboxfake.New()
		if err := sb.Create(context.Background(), goatherdsandbox.SandboxSpec{}); err != nil {
			t.Fatalf("create fake: %v", err)
		}
		sb.ExecHook = func(req goatherdsandbox.ExecRequest) (goatherdsandbox.ExecResult, error) {
			if req.Command != "git" {
				return goatherdsandbox.ExecResult{ExitCode: 127}, nil
			}
			switch strings.Join(req.Args, " ") {
			case "rev-parse HEAD":
				return goatherdsandbox.ExecResult{Stdout: "base\n"}, nil
			case "write-tree":
				return goatherdsandbox.ExecResult{Stdout: "tree\n"}, nil
			case "cat-file -s 333":
				return goatherdsandbox.ExecResult{Stdout: strconv.Itoa(gitDeltaMaxContentBytes+1) + "\n"}, nil
			}
			if strings.HasPrefix(strings.Join(req.Args, " "), "diff-tree") {
				return goatherdsandbox.ExecResult{Stdout: ":000000 100644 0 333 A\x00big.bin\x00"}, nil
			}
			return goatherdsandbox.ExecResult{ExitCode: 0}, nil
		}
		_, _, err := captureGitDelta(context.Background(), sb, "/", "base")
		if err == nil || !strings.Contains(err.Error(), "cap") {
			t.Fatalf("error = %v, want the content cap", err)
		}
	})

	t.Run("unsafe path", func(t *testing.T) {
		sb := sandboxfake.New()
		if err := sb.Create(context.Background(), goatherdsandbox.SandboxSpec{}); err != nil {
			t.Fatalf("create fake: %v", err)
		}
		sb.ExecHook = func(req goatherdsandbox.ExecRequest) (goatherdsandbox.ExecResult, error) {
			key := strings.Join(req.Args, " ")
			switch {
			case req.Command == "git" && key == "rev-parse HEAD":
				return goatherdsandbox.ExecResult{Stdout: "base\n"}, nil
			case req.Command == "git" && key == "write-tree":
				return goatherdsandbox.ExecResult{Stdout: "tree\n"}, nil
			case req.Command == "git" && strings.HasPrefix(key, "diff-tree"):
				return goatherdsandbox.ExecResult{Stdout: ":000000 100644 0 333 A\x00../escape.txt\x00"}, nil
			default:
				return goatherdsandbox.ExecResult{ExitCode: 0}, nil
			}
		}
		if _, _, err := captureGitDelta(context.Background(), sb, "/", "base"); err == nil {
			t.Fatal("an unsafe path from git must be refused")
		}
	})
}

// containerHookGit is a canned git responder for the fake dispatch sandbox. It
// returns the base commit, tree, raw diff, blob, and review the producer asks
// for; everything else is an unexpected-command failure.
func containerHookGit(head, tree, raw, review string, blob []byte) func(goatherdsandbox.ExecRequest) (goatherdsandbox.ExecResult, error) {
	ok := func(out string) (goatherdsandbox.ExecResult, error) {
		return goatherdsandbox.ExecResult{ExitCode: 0, Stdout: out}, nil
	}
	return func(req goatherdsandbox.ExecRequest) (goatherdsandbox.ExecResult, error) {
		switch req.Command {
		case "git":
			key := strings.Join(req.Args, " ")
			switch {
			case key == "rev-parse HEAD":
				return ok(head + "\n")
			case key == "add -A":
				return ok("")
			case key == "write-tree":
				return ok(tree + "\n")
			case strings.HasPrefix(key, "diff-tree "):
				return ok(raw)
			case strings.HasPrefix(key, "cat-file -s "):
				return ok(strconv.Itoa(len(blob)) + "\n")
			case strings.HasPrefix(key, "diff "):
				return ok(review)
			}
		case "sh":
			if len(req.Args) > 1 && strings.Contains(req.Args[1], "cat-file") {
				return ok(base64.StdEncoding.EncodeToString(blob) + "\n")
			}
		}
		return goatherdsandbox.ExecResult{ExitCode: 127, Stderr: "unexpected command: " + strings.Join(append([]string{req.Command}, req.Args...), " ")}, nil
	}
}

// containerProposalRuntime builds a host-session runtime whose proposal
// dispatches resolve to a container backed by a fake sandbox.
func containerProposalRuntime(t *testing.T, fx *sessionFixtures, repo, rootID string, runner TaskRunner, hook func(goatherdsandbox.ExecRequest) (goatherdsandbox.ExecResult, error)) (RetainedRuntime, func() []*sandboxfake.Sandbox) {
	t.Helper()
	var opened []*sandboxfake.Sandbox
	open := func(config.WorkspaceConfig) (goatherdsandbox.Sandbox, error) {
		sb := sandboxfake.New()
		if len(opened) == 0 {
			// The image-contract probe runs on its own sandbox.
			sb.ExecHook = func(goatherdsandbox.ExecRequest) (goatherdsandbox.ExecResult, error) {
				return goatherdsandbox.ExecResult{ExitCode: 0}, nil
			}
		} else {
			sb.ExecHook = hook
		}
		opened = append(opened, sb)
		return sb, nil
	}
	rt := RetainedRuntime{
		Runner:          runner,
		RepoPath:        repo,
		ScopeManager:    fx.mgr,
		RootScopeID:     rootID,
		WorktreeRoot:    t.TempDir(),
		Proposals:       NewProposalRegistry(),
		World:           "container",
		ContainerWorlds: true,
		Containment:     "auto",
		Network:         "allow_all",
		DispatchWorlds:  &DispatchWorlds{Config: sessionTestConfig(), Opener: open},
	}
	return rt, func() []*sandboxfake.Sandbox { return opened }
}

// TestContainerProposal_EndToEnd covers the container proposal lifecycle over a
// fake sandbox: bootstrap, base capture, body, seal, persist, destroy, then a
// real host settlement. No containerd daemon is involved; git inside the
// container is canned, but every yaah-side step is real.
func TestContainerProposal_EndToEnd(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	head, tree := "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	content := []byte("from the container\n")
	raw := ":000000 100644 0000000 3333333 A\x00out.txt\x00"
	review := "diff --git a/out.txt b/out.txt\nnew file mode 100644\n--- /dev/null\n+++ b/out.txt\n@@ -0,0 +1 @@\n+from the container\n"

	var fakes func() []*sandboxfake.Sandbox
	run := func(ctx context.Context, _ string, params SubAgentParams) (string, error) {
		ws := DispatchWorkspace(ctx)
		if ws == nil {
			t.Fatal("a container proposal body must run against the dispatch workspace")
		}
		if params.Workdir != ws.WorkDir() {
			t.Errorf("workdir = %q, want %q", params.Workdir, ws.WorkDir())
		}
		// Write straight through the dispatch sandbox: the fake's ExecHook is
		// already answering git, so the workspace's shell write would need a
		// second interpreter. The seal path under test is unaffected.
		opened := fakes()
		if len(opened) < 2 {
			t.Fatalf("dispatch sandbox not opened (have %d)", len(opened))
		}
		if err := opened[1].WriteFile(ctx, ws.Join(ws.WorkDir(), "out.txt"), content, 0o644); err != nil {
			return "", err
		}
		return "the body's report", nil
	}
	rt, fakeGetter := containerProposalRuntime(t, fx, repo, rootID, run, containerHookGit(head, tree, raw, review, content))
	fakes = fakeGetter

	res, err := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "developer"}, 0, false)
	if err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	env := decodeProposalEnvelope(t, res)
	if env.Status != "proposal" || env.World != "container" || env.ProposalID == "" {
		t.Fatalf("envelope = %+v, want a container proposal", env)
	}
	if strings.Join(env.Files, ",") != "out.txt" {
		t.Errorf("files = %v, want [out.txt]", env.Files)
	}
	if !strings.Contains(env.Diff, "+from the container") {
		t.Errorf("diff must be reviewable:\n%s", env.Diff)
	}
	if env.Result != "the body's report" {
		t.Errorf("result = %q (error = %q notes = %q)", env.Result, env.Error, env.Notes)
	}
	if _, err := os.Stat(filepath.Join(repo, "out.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the host must be untouched before settlement (err=%v)", err)
	}

	// The manifest survives the container, and the container does not survive
	// the seal.
	manifest := filepath.Join(proposalArtifactDir(repo, env.ProposalID), "delta.json")
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("manifest not persisted: %v", err)
	}
	if !strings.Contains(string(data), "out.txt") {
		t.Errorf("manifest does not name the changed path:\n%s", data)
	}
	for i, sb := range fakes() {
		if !sb.Destroyed() {
			t.Errorf("sandbox %d must be destroyed at seal", i)
		}
	}

	// list/show work for a delta proposal: the world is gone, the manifest is
	// the proposal.
	tool := &ProposalTool{Proposals: rt.Proposals}
	listed, err := tool.Execute(context.Background(), `{"action":"list"}`)
	if err != nil || !strings.Contains(listed, env.ProposalID) {
		t.Fatalf("list must name the delta proposal: %q err=%v", listed, err)
	}
	shown, err := tool.Execute(context.Background(), `{"action":"show","proposal_id":`+jsonString(env.ProposalID)+`}`)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	shownEnv := decodeProposalEnvelope(t, shown)
	if shownEnv.World != "container" || strings.Join(shownEnv.Files, ",") != "out.txt" || !strings.Contains(shownEnv.Diff, "+from the container") {
		t.Fatalf("show envelope = %+v, want the container world, files, and review", shownEnv)
	}

	// select lands the manifest on the real host repository.
	settled, err := rt.Proposals.settleRetainedProposal(context.Background(), env.ProposalID, "select")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	senv := decodeProposalEnvelope(t, settled)
	if senv.Status != "settled" || senv.World != "container" {
		t.Fatalf("settled envelope = %+v", senv)
	}
	if got, err := os.ReadFile(filepath.Join(repo, "out.txt")); err != nil || string(got) != string(content) {
		t.Errorf("host out.txt = %q, %v", got, err)
	}
	if _, err := os.Stat(proposalArtifactDir(repo, env.ProposalID)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the manifest must be consumed by select (err=%v)", err)
	}
}

// TestContainerProposal_SealFailureDestroysContainer proves a failed seal leaves
// no proposal and no live world.
func TestContainerProposal_SealFailureDestroysContainer(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	failing := func(req goatherdsandbox.ExecRequest) (goatherdsandbox.ExecResult, error) {
		switch {
		case req.Command == "git" && strings.Join(req.Args, " ") == "rev-parse HEAD":
			return goatherdsandbox.ExecResult{ExitCode: 0, Stdout: "abc\n"}, nil
		case req.Command == "git" && strings.Join(req.Args, " ") == "add -A":
			return goatherdsandbox.ExecResult{ExitCode: 0}, nil
		default:
			return goatherdsandbox.ExecResult{ExitCode: 1, Stderr: "write-tree exploded"}, nil
		}
	}
	run := func(ctx context.Context, _ string, _ SubAgentParams) (string, error) {
		ws := DispatchWorkspace(ctx)
		return "partial", ws.WriteFile(ctx, ws.Join(ws.WorkDir(), "out.txt"), []byte("x"), 0o644)
	}
	rt, fakes := containerProposalRuntime(t, fx, repo, rootID, run, failing)

	res, err := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "developer"}, 0, false)
	if err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	env := decodeProposalEnvelope(t, res)
	if env.Status != "error" || !strings.Contains(env.Error, "write-tree exploded") {
		t.Fatalf("envelope = %+v, want a seal error", env)
	}
	if ps := rt.Proposals.all(); len(ps) != 0 {
		t.Errorf("a failed seal must register no proposal, got %d", len(ps))
	}
	for i, sb := range fakes() {
		if !sb.Destroyed() {
			t.Errorf("sandbox %d must be destroyed after a failed seal", i)
		}
	}
	entries, rerr := os.ReadDir(filepath.Join(repo, ".yaah", "proposals"))
	if rerr == nil && len(entries) != 0 {
		t.Errorf("a failed seal must persist nothing, found %v", entries)
	}
}

// TestGitDeltaSettle_ApplyIgnoresPreExistingDirty pins the basis semantics: a
// file that was already dirty at dispatch is part of the basis, so a proposal
// touching it is not a collision when the host has not moved since.
func TestGitDeltaSettle_ApplyIgnoresPreExistingDirty(t *testing.T) {
	f := newDeltaFixtureWith(t, func(repo string) {
		if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("host dirty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	delta := &GitDelta{
		BasisHead: "basis",
		Changes: []DeltaChange{{
			Path:    "shared.txt",
			Kind:    "modify",
			Mode:    0o644,
			Content: []byte("host dirty\nsub-agent edit\n"),
		}},
	}
	f.add(t, "proposal:dirty-basis", delta)

	f.settle(t, "proposal:dirty-basis", "apply")
	got, err := os.ReadFile(filepath.Join(f.repo, "shared.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "host dirty\nsub-agent edit\n" {
		t.Fatalf("content = %q", got)
	}
}

// TestGitDelta_PathsSortedDeduped pins Paths()'s contract for manifests that
// did not come from the producer (e.g. read from disk in a different order).
func TestGitDelta_PathsSortedDeduped(t *testing.T) {
	d := &GitDelta{Changes: []DeltaChange{
		{Path: "z.txt"}, {Path: "a.txt"}, {Path: "z.txt"}, {Path: "m/n.txt"},
	}}
	got := d.Paths()
	want := []string{"a.txt", "m/n.txt", "z.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Paths() = %v, want %v", got, want)
	}
}

// TestNormalizeHostDiffPaths pins the collision guard's backend precondition:
// only git's workdir-relative paths are comparable to manifest paths.
func TestNormalizeHostDiffPaths(t *testing.T) {
	got, err := normalizeHostDiffPaths("git", []string{"./src/a.go", "docs//b.md"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "src/a.go,docs/b.md" {
		t.Fatalf("normalized = %v", got)
	}

	if _, err := normalizeHostDiffPaths("containerd", []string{"/workspace/a.go"}); err == nil {
		t.Fatal("a non-git host backend must fail closed, not compare incomparable paths")
	}
}
