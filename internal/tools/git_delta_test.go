package tools

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/goatherd-runtime/effect"
	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/sandboxfake"
)

// git_delta_test.go covers the container settlement artifact without a
// containerd daemon: the path and manifest validation, the host writer, the
// persistent proposal lifecycle, and one end-to-end dispatch over a fake
// sandbox. The fake supplies the snapshot change set and post-images, so every
// step — bootstrap copy-in, baseline capture, seal, persist, destroy, settle —
// is exercised without git inside the container.

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
	// A snapshot change set reports the file's real permission bits, not git's
	// normalized 0644/0755, so a private file is a legal post-image.
	private := &GitDelta{Changes: []DeltaChange{{Path: "private.txt", Kind: deltaKindAdd, Mode: 0o600, Content: []byte("s")}}}
	if err := validateDelta(private); err != nil {
		t.Fatalf("a 0600 post-image must be valid: %v", err)
	}

	cases := map[string]*GitDelta{
		"duplicate paths": {Changes: []DeltaChange{
			{Path: "a.txt", Kind: deltaKindAdd, Mode: 0o644},
			{Path: "a.txt", Kind: deltaKindModify, Mode: 0o644},
		}},
		"unknown kind":        {Changes: []DeltaChange{{Path: "a.txt", Kind: "rename", Mode: 0o644}}},
		"delete with content": {Changes: []DeltaChange{{Path: "a.txt", Kind: deltaKindDelete, Content: []byte("x")}}},
		"symlink mode":        {Changes: []DeltaChange{{Path: "a.txt", Kind: deltaKindAdd, Mode: fs.ModeSymlink | 0o777}}},
		"directory mode":      {Changes: []DeltaChange{{Path: "a.txt", Kind: deltaKindAdd, Mode: fs.ModeDir | 0o755}}},
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

// newDeltaSession builds a SessionSandbox around an already-created fake whose
// workspace root is /workspace. Callers seed the tree and then call
// captureDeltaBaseline, so the baseline includes the seed.
func newDeltaSession(t *testing.T, sb *sandboxfake.Sandbox) *SessionSandbox {
	t.Helper()
	if err := sb.Create(context.Background(), goatherdsandbox.SandboxSpec{Workdir: "/workspace"}); err != nil {
		t.Fatalf("create fake: %v", err)
	}
	return &SessionSandbox{sb: sb, ws: newSandboxWorkspace(sb, "/workspace")}
}

// captureDeltaBaseline records the current tree as the session baseline.
func captureDeltaBaseline(t *testing.T, ss *SessionSandbox) {
	t.Helper()
	base, err := ss.sb.Capture(context.Background())
	if err != nil {
		t.Fatalf("capture baseline: %v", err)
	}
	ss.baseline = &base
}

func TestCaptureContainerDelta_ProducesManifest(t *testing.T) {
	sb := sandboxfake.New()
	ss := newDeltaSession(t, sb)
	if err := sb.WriteFile(context.Background(), "/workspace/keep.txt", []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sb.WriteFile(context.Background(), "/workspace/gone.txt", []byte("bye\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	captureDeltaBaseline(t, ss)

	if err := ss.ws.WriteFile(context.Background(), "/workspace/keep.txt", []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ss.ws.WriteFile(context.Background(), "/workspace/out.txt", []byte("from the container\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ss.ws.Remove(context.Background(), "/workspace/gone.txt"); err != nil {
		t.Fatal(err)
	}

	delta, review, err := captureContainerDelta(context.Background(), ss)
	if err != nil {
		t.Fatalf("captureContainerDelta: %v", err)
	}
	if delta.BasisRevision == "" {
		t.Error("the manifest must record the baseline revision")
	}
	got := make(map[string]DeltaChange, len(delta.Changes))
	for _, c := range delta.Changes {
		got[c.Path] = c
	}
	if len(got) != 3 {
		t.Fatalf("changes = %+v, want keep.txt, out.txt, gone.txt", delta.Changes)
	}
	if c := got["out.txt"]; c.Kind != deltaKindAdd || string(c.Content) != "from the container\n" || c.Mode.Perm() != 0o644 {
		t.Errorf("out.txt = %+v, want an add with its post-image", c)
	}
	if c := got["keep.txt"]; c.Kind != deltaKindModify || string(c.Content) != "after\n" || c.Mode.Perm() != 0o600 {
		t.Errorf("keep.txt = %+v, want a modify carrying the real 0600 mode", c)
	}
	if c := got["gone.txt"]; c.Kind != deltaKindDelete || len(c.Content) != 0 {
		t.Errorf("gone.txt = %+v, want a delete with no content", c)
	}
	if !strings.Contains(review, "+from the container") {
		t.Errorf("review must render the added post-image:\n%s", review)
	}
	if !strings.Contains(review, "-before") || !strings.Contains(review, "+after") {
		t.Errorf("review must show the real modify delta, not only the post-image:\n%s", review)
	}
}

func TestCaptureContainerDelta_ReviewShowsDeltaAlongsideNewDirectory(t *testing.T) {
	sb := sandboxfake.New()
	ss := newDeltaSession(t, sb)
	if err := sb.WriteFile(context.Background(), "/workspace/file.txt", []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	captureDeltaBaseline(t, ss)
	if err := ss.ws.WriteFile(context.Background(), "/workspace/file.txt", []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ss.ws.MkdirAll(context.Background(), "/workspace/newdir", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ss.ws.WriteFile(context.Background(), "/workspace/newdir/new.txt", []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, review, err := captureContainerDelta(context.Background(), ss)
	if err != nil {
		t.Fatalf("captureContainerDelta: %v", err)
	}
	if !strings.Contains(review, "-before") || !strings.Contains(review, "+after") {
		t.Fatalf("review must retain the real modify delta despite a directory entry:\n%s", review)
	}
}

func TestCaptureContainerDelta_SkipsBookkeepingAndDirectories(t *testing.T) {
	sb := sandboxfake.New()
	ss := newDeltaSession(t, sb)
	for p, body := range map[string]string{
		"/workspace/.git/index":                    "old-index",
		"/workspace/.yaah/proposals/p1/delta.json": "old-manifest",
		"/workspace/.yaah/scratch/state":           "old-state",
	} {
		if err := sb.WriteFile(context.Background(), p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	captureDeltaBaseline(t, ss)

	// A git command in the container rewrites index/log files, and a proposal
	// must not settle its own registry; both are bookkeeping, not content.
	if err := sb.WriteFile(context.Background(), "/workspace/.git/index", []byte("new-index"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sb.WriteFile(context.Background(), "/workspace/.yaah/proposals/p1/delta.json", []byte("new-manifest"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sb.WriteFile(context.Background(), "/workspace/.yaah/scratch/state", []byte("new-state"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ss.ws.MkdirAll(context.Background(), "/workspace/newdir", 0o755); err != nil {
		t.Fatal(err)
	}

	delta, _, err := captureContainerDelta(context.Background(), ss)
	if err != nil {
		t.Fatalf("captureContainerDelta: %v", err)
	}
	if len(delta.Changes) != 0 {
		t.Fatalf("changes = %+v, want no bookkeeping or directory entries", delta.Changes)
	}
}

func TestCaptureContainerDelta_SkipsDeniedPaths(t *testing.T) {
	sb := sandboxfake.New()
	ss := newDeltaSession(t, sb)
	ss.deny = []string{".env"}
	for p, body := range map[string]string{
		"/workspace/.env":   "secret\n",
		"/workspace/ok.txt": "ok\n",
	} {
		if err := sb.WriteFile(context.Background(), p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	captureDeltaBaseline(t, ss)
	if err := sb.WriteFile(context.Background(), "/workspace/.env", []byte("new secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sb.WriteFile(context.Background(), "/workspace/ok.txt", []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	delta, _, err := captureContainerDelta(context.Background(), ss)
	if err != nil {
		t.Fatalf("captureContainerDelta: %v", err)
	}
	if len(delta.Changes) != 1 || delta.Changes[0].Path != "ok.txt" {
		t.Fatalf("changes = %+v, want only the non-denied file", delta.Changes)
	}
}

func TestCopyOut_SkipsBookkeepingAndDeniedPaths(t *testing.T) {
	sb := sandboxfake.New()
	ss := newDeltaSession(t, sb)
	ss.deny = []string{".env"}
	if err := sb.WriteFile(context.Background(), "/workspace/keep.txt", []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	captureDeltaBaseline(t, ss)
	for p, body := range map[string]string{
		"/workspace/.git/index":    "index",
		"/workspace/.yaah/scratch": "scratch",
		"/workspace/.env":          "secret\n",
		"/workspace/keep.txt":      "after\n",
	} {
		if err := sb.WriteFile(context.Background(), p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	host := t.TempDir()
	if err := ss.copyOut(context.Background(), host); err != nil {
		t.Fatalf("copyOut: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(host, "keep.txt"))
	if err != nil || string(got) != "after\n" {
		t.Fatalf("keep.txt = %q, %v; want the workspace change", got, err)
	}
	for _, rel := range []string{".git/index", ".yaah/scratch", ".env"} {
		if _, err := os.Stat(filepath.Join(host, rel)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s must not reach the host (err=%v)", rel, err)
		}
	}
}

func TestCaptureContainerDelta_RequiresBaseline(t *testing.T) {
	sb := sandboxfake.New()
	if err := sb.Create(context.Background(), goatherdsandbox.SandboxSpec{Workdir: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	ss := &SessionSandbox{sb: sb, ws: newSandboxWorkspace(sb, "/workspace")}
	if _, _, err := captureContainerDelta(context.Background(), ss); err == nil || !strings.Contains(err.Error(), "baseline") {
		t.Fatalf("error = %v, want a missing-baseline refusal", err)
	}
}

func TestManifestChange_RefusesOversizedSymlinkAndUnsafe(t *testing.T) {
	sb := sandboxfake.New()
	ss := newDeltaSession(t, sb)
	captureDeltaBaseline(t, ss)

	t.Run("oversized before transfer", func(t *testing.T) {
		// The path does not exist: if the size check did not run first, the
		// read would fail with a different error and the test would say so.
		_, err := manifestChange(context.Background(), ss, effect.Change{
			Path: "big.bin", Kind: effect.KindCreated, Size: gitDeltaMaxContentBytes + 1,
		}, gitDeltaMaxContentBytes)
		if err == nil || !strings.Contains(err.Error(), "cap") {
			t.Fatalf("error = %v, want the content cap before any read", err)
		}
	})

	t.Run("symlink post-image", func(t *testing.T) {
		if err := sb.Symlink("/workspace/target", "/workspace/link"); err != nil {
			t.Fatal(err)
		}
		_, err := manifestChange(context.Background(), ss, effect.Change{
			Path: "link", Kind: effect.KindCreated, Mode: fs.ModeSymlink | 0o777,
		}, gitDeltaMaxContentBytes)
		if err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("error = %v, want a symlink refusal", err)
		}
	})

	t.Run("special file", func(t *testing.T) {
		_, err := manifestChange(context.Background(), ss, effect.Change{
			Path: "fifo", Kind: effect.KindCreated, Mode: fs.ModeNamedPipe | 0o600,
		}, gitDeltaMaxContentBytes)
		if err == nil || !strings.Contains(err.Error(), "regular-file") {
			t.Fatalf("error = %v, want a special-file refusal before any read", err)
		}
	})

	t.Run("unsafe path", func(t *testing.T) {
		_, err := manifestChange(context.Background(), ss, effect.Change{
			Path: "../escape.txt", Kind: effect.KindCreated, Size: 1,
		}, gitDeltaMaxContentBytes)
		if err == nil {
			t.Fatal("an escaping change path must be refused")
		}
	})

	t.Run("absolute path outside the root", func(t *testing.T) {
		_, err := manifestChange(context.Background(), ss, effect.Change{
			Path: "/etc/passwd", Kind: effect.KindCreated, Size: 1,
		}, gitDeltaMaxContentBytes)
		if err == nil || !strings.Contains(err.Error(), "outside the workspace root") {
			t.Fatalf("error = %v, want an outside-the-root refusal", err)
		}
	})
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
// fake sandbox: bootstrap, baseline capture, body, snapshot seal, persist,
// destroy, then a real host settlement. No containerd daemon and no git inside
// the container: the producer reads the fake's change set and post-images, so
// every step is real.
func TestContainerProposal_EndToEnd(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	content := []byte("from the container\n")

	run := func(ctx context.Context, _ string, params SubAgentParams) (string, error) {
		ws := DispatchWorkspace(ctx)
		if ws == nil {
			t.Fatal("a container proposal body must run against the dispatch workspace")
		}
		if params.Workdir != ws.WorkDir() {
			t.Errorf("workdir = %q, want %q", params.Workdir, ws.WorkDir())
		}
		if err := ws.WriteFile(ctx, ws.Join(ws.WorkDir(), "out.txt"), content, 0o644); err != nil {
			return "", err
		}
		return "the body's report", nil
	}
	rt, fakes := containerProposalRuntime(t, fx, repo, rootID, run, nil)

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
// no proposal and no live world. A symlink post-image is the one change a
// whole-path file manifest cannot represent, so the seal fails after the body
// has run, exercising the real cleanup path rather than a canned error.
func TestContainerProposal_SealFailureDestroysContainer(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	var fakes func() []*sandboxfake.Sandbox
	run := func(ctx context.Context, _ string, _ SubAgentParams) (string, error) {
		opened := fakes()
		if len(opened) < 2 {
			t.Fatalf("dispatch sandbox not opened (have %d)", len(opened))
		}
		return "partial", opened[1].Symlink("/workspace/target", "/workspace/link")
	}
	rt, getter := containerProposalRuntime(t, fx, repo, rootID, run, nil)
	fakes = getter

	res, err := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "developer"}, 0, false)
	if err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	env := decodeProposalEnvelope(t, res)
	if env.Status != "error" || !strings.Contains(env.Error, "symlink") {
		t.Fatalf("envelope = %+v, want a seal error naming the symlink", env)
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
		BasisRevision: "basis",
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
