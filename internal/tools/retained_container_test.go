package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
	"github.com/buchenberg/yaah/internal/config"
)

// containerReportRuntime builds a host-session retained runtime whose report
// dispatches resolve to a per-dispatch container world backed by open.
func containerReportRuntime(t *testing.T, fx *sessionFixtures, repo, rootID, worktreeRoot string, runner TaskRunner, world string, open SandboxOpener) RetainedRuntime {
	t.Helper()
	return RetainedRuntime{
		Runner:          runner,
		RepoPath:        repo,
		ScopeManager:    fx.mgr,
		RootScopeID:     rootID,
		WorktreeRoot:    worktreeRoot,
		Proposals:       NewProposalRegistry(),
		World:           world,
		ContainerWorlds: true,
		Containment:     "auto",
		Network:         "allow_all",
		DispatchWorlds: &DispatchWorlds{
			Config: sessionTestConfig(),
			Opener: open,
		},
	}
}

// TestRetainedReport_ContainerWorld proves the report-lane container path end to
// end over the in-memory sandbox: the body runs against the dispatch workspace
// (not a host worktree), the world is recorded with the dispatch, and both the
// scope and the container come down afterwards. No daemon is involved.
func TestRetainedReport_ContainerWorld(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	open, fakes := cleanOpener()
	worktreeRoot := t.TempDir()

	var (
		calls      int
		gotWS      Workspace
		gotWorkdir string
	)
	run := func(ctx context.Context, _ string, params SubAgentParams) (string, error) {
		calls++
		gotWS = DispatchWorkspace(ctx)
		gotWorkdir = params.Workdir
		return "container report", nil
	}
	rt := containerReportRuntime(t, fx, repo, rootID, worktreeRoot, run, "auto", open)

	res, err := startRetainedRun(context.Background(), rt, "inspect the build", SubAgentParams{Role: "reviewer"}, 0, true)
	if err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	if calls != 1 {
		t.Fatalf("runner called %d times, want 1", calls)
	}
	if res != "container report" {
		t.Errorf("result = %q, want the body's report", res)
	}
	if gotWS == nil {
		t.Fatal("the runner must receive the dispatch workspace in its context")
	}
	if gotWS.Local() {
		t.Error("a container dispatch workspace must not report as local")
	}
	if gotWorkdir != gotWS.WorkDir() {
		t.Errorf("Workdir = %q, want the dispatch workspace's root %q", gotWorkdir, gotWS.WorkDir())
	}

	// Probe sandbox plus the dispatch sandbox: both must be destroyed.
	opened := fakes()
	if len(opened) != 2 {
		t.Fatalf("opener called %d times, want 2 (probe then dispatch sandbox)", len(opened))
	}
	for i, sb := range opened {
		if !sb.Destroyed() {
			t.Errorf("sandbox %d must be destroyed after the dispatch", i)
		}
	}

	// A report dispatch registers no proposal and cuts no worktree.
	if ps := rt.Proposals.all(); len(ps) != 0 {
		t.Errorf("a report dispatch must register no proposal, got %d", len(ps))
	}
	if entries, rerr := os.ReadDir(worktreeRoot); rerr == nil && len(entries) != 0 {
		t.Errorf("a container dispatch must not cut a worktree; %s holds %d entries", worktreeRoot, len(entries))
	}
}

// TestRetainedReport_ContainerPreflightFallsBackForAuto pins the preflight
// contract: when auto resolved a container but the world cannot be provisioned,
// the dispatch steps down to the worktree before any work starts, and the
// report carries the recorded degradation.
func TestRetainedReport_ContainerPreflightFallsBackForAuto(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	worktreeRoot := t.TempDir()

	open := func(config.WorkspaceConfig) (goatherdsandbox.Sandbox, error) {
		return nil, errors.New("daemon unreachable")
	}
	var gotWorkdir string
	run := func(_ context.Context, _ string, params SubAgentParams) (string, error) {
		gotWorkdir = params.Workdir
		return "fell back report", nil
	}
	rt := containerReportRuntime(t, fx, repo, rootID, worktreeRoot, run, "auto", open)

	res, err := startRetainedRun(context.Background(), rt, "inspect the build", SubAgentParams{Role: "reviewer"}, 0, true)
	if err != nil {
		t.Fatalf("auto must degrade, not fail: %v", err)
	}
	for _, want := range []string{"fell back report", "dispatch world degraded", "daemon unreachable"} {
		if !strings.Contains(res, want) {
			t.Errorf("report must carry %q, got:\n%s", want, res)
		}
	}
	if rel, rerr := filepath.Rel(worktreeRoot, gotWorkdir); rerr != nil || strings.HasPrefix(rel, "..") {
		t.Errorf("the fallback must run in a worktree under %s, got workdir %q", worktreeRoot, gotWorkdir)
	}
}

// TestRetainedReport_ContainerPreflightFailsClosed pins the other half: an
// explicit container request is never silently re-run in another world, and the
// failure happens before the body starts.
func TestRetainedReport_ContainerPreflightFailsClosed(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	worktreeRoot := t.TempDir()

	open := func(config.WorkspaceConfig) (goatherdsandbox.Sandbox, error) {
		return nil, errors.New("daemon unreachable")
	}
	calls := 0
	run := func(context.Context, string, SubAgentParams) (string, error) {
		calls++
		return "must not run", nil
	}
	rt := containerReportRuntime(t, fx, repo, rootID, worktreeRoot, run, "container", open)

	if _, err := startRetainedRun(context.Background(), rt, "inspect the build", SubAgentParams{Role: "reviewer"}, 0, true); err == nil {
		t.Fatal("an explicit container request must fail closed")
	} else if !strings.Contains(err.Error(), "daemon unreachable") {
		t.Errorf("error = %v, want it to name the preflight failure", err)
	}
	if calls != 0 {
		t.Errorf("the body must never run when the container preflight fails, ran %d times", calls)
	}
	if entries, rerr := os.ReadDir(worktreeRoot); rerr == nil && len(entries) != 0 {
		t.Errorf("a failed explicit container must not fall back to a worktree; %s holds %d entries", worktreeRoot, len(entries))
	}
}
