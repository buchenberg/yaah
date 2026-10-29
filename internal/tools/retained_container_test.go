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

// TestRetainedReport_ContainerSessionSibling proves the container-session
// slice: a report dispatch gets its own sibling container seeded from the
// session container's captured state, while the session sandbox stays alive.
func TestRetainedReport_ContainerSessionSibling(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)

	sessionOpen, sessionFakes := cleanOpener()
	session, err := StartSessionSandbox(context.Background(), sessionTestConfig(), sessionOpen, "", nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox(session): %v", err)
	}
	if err := session.sb.WriteFile(context.Background(), "seed.txt", []byte("from the session\n"), 0o644); err != nil {
		t.Fatalf("seed session workspace: %v", err)
	}
	sessionOpened := sessionFakes()
	if len(sessionOpened) != 2 {
		t.Fatalf("session opener calls = %d, want 2", len(sessionOpened))
	}

	dispatchOpen, dispatchFakes := cleanOpener()
	var gotSeed string
	run := func(ctx context.Context, _ string, params SubAgentParams) (string, error) {
		ws := DispatchWorkspace(ctx)
		if ws == nil || ws.Local() {
			return "", errors.New("a sibling dispatch must run in a container workspace")
		}
		data, err := ws.ReadFile(ctx, "seed.txt")
		if err != nil {
			return "", err
		}
		gotSeed = string(data)
		return "saw the session state", nil
	}
	rt := RetainedRuntime{
		Runner:          run,
		RepoPath:        repo,
		ScopeManager:    fx.mgr,
		RootScopeID:     rootID,
		WorktreeRoot:    t.TempDir(),
		Proposals:       NewProposalRegistry(),
		World:           "auto",
		ContainerWorlds: true,
		Containment:     "auto",
		Network:         "allow_all",
		DispatchWorlds: &DispatchWorlds{
			Config:         sessionTestConfig(),
			Opener:         dispatchOpen,
			SessionSandbox: session,
		},
	}

	res, err := startRetainedRun(context.Background(), rt, "inspect", SubAgentParams{Role: "reviewer"}, 0, true)
	if err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	if res != "saw the session state" {
		t.Fatalf("result = %q", res)
	}
	if gotSeed != "from the session\n" {
		t.Fatalf("sibling seed = %q, want the session container's state", gotSeed)
	}
	if len(rt.Proposals.all()) != 0 {
		t.Fatal("a report dispatch must not register a proposal")
	}
	if sessionOpened[1].Destroyed() {
		t.Error("the session container must survive a report dispatch")
	}
	for i, sb := range dispatchFakes() {
		if !sb.Destroyed() {
			t.Errorf("dispatch sandbox %d must be destroyed", i)
		}
	}
}

// TestRetainedReport_ContainerSessionFallsBackToSession pins the availability
// rule: if a container session's sibling cannot be provisioned, an auto report
// still runs in the session container with a recorded degradation.
func TestRetainedReport_ContainerSessionFallsBackToSession(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	var gotWS Workspace
	run := func(ctx context.Context, _ string, _ SubAgentParams) (string, error) {
		gotWS = DispatchWorkspace(ctx)
		return "session fallback report", nil
	}
	rt := containerReportRuntime(t, fx, repo, rootID, t.TempDir(), run, "auto", nil)
	rt.DispatchWorlds.SessionSandbox = &SessionSandbox{}

	res, err := startRetainedRun(context.Background(), rt, "inspect", SubAgentParams{Role: "reviewer"}, 0, true)
	if err != nil {
		t.Fatalf("auto report must fall back to the session container: %v", err)
	}
	for _, want := range []string{"session fallback report", "dispatch world degraded"} {
		if !strings.Contains(res, want) {
			t.Errorf("result must contain %q, got:\n%s", want, res)
		}
	}
	if gotWS != nil {
		t.Error("the session-container fallback must not inject a dispatch workspace")
	}
}

// TestRetainedReport_ContainerSessionPolicyFallback pins the availability rule
// for a policy-refused sibling (deny_all cannot be enforced in a container):
// an auto report still runs in the session container, with the reason recorded.
func TestRetainedReport_ContainerSessionPolicyFallback(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	var dispatched bool
	run := func(context.Context, string, SubAgentParams) (string, error) {
		dispatched = true
		return "policy fallback report", nil
	}
	rt := containerReportRuntime(t, fx, repo, rootID, t.TempDir(), run, "auto", nil)
	rt.DispatchWorlds.SessionSandbox = &SessionSandbox{}
	rt.Network = "deny_all"

	res, err := startRetainedRun(context.Background(), rt, "inspect", SubAgentParams{Role: "reviewer"}, 0, true)
	if err != nil {
		t.Fatalf("auto report must fall back to the session container: %v", err)
	}
	if !dispatched {
		t.Fatal("the report body did not run")
	}
	for _, want := range []string{"policy fallback report", "degraded", "network"} {
		if !strings.Contains(res, want) {
			t.Errorf("result must contain %q, got:\n%s", want, res)
		}
	}
}

func TestRetainedReport_ContainerSessionUnknownWorldDoesNotFallback(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	run := func(context.Context, string, SubAgentParams) (string, error) {
		t.Fatal("an unknown world must not run")
		return "", nil
	}
	rt := containerReportRuntime(t, fx, repo, rootID, t.TempDir(), run, "podman", nil)
	rt.DispatchWorlds.SessionSandbox = &SessionSandbox{}
	if _, err := startRetainedRun(context.Background(), rt, "inspect", SubAgentParams{Role: "reviewer"}, 0, true); err == nil {
		t.Fatal("an unknown world must surface as an error, not fall back")
	}
}

func TestRetainedReport_ContainerSessionExplicitWorktreeFails(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	run := func(context.Context, string, SubAgentParams) (string, error) {
		t.Fatal("an unavailable explicit worktree must not run")
		return "", nil
	}
	rt := containerReportRuntime(t, fx, repo, rootID, t.TempDir(), run, "worktree", nil)
	rt.DispatchWorlds.SessionSandbox = &SessionSandbox{}
	if _, err := startRetainedRun(context.Background(), rt, "inspect", SubAgentParams{Role: "reviewer"}, 0, true); err == nil {
		t.Fatal("an explicit worktree in a container session must fail closed")
	}
}

// noReleaseSandbox exposes only the Sandbox interface methods, hiding a
// concrete backend's ReleaseState so the capture-releasability guard is
// exercised.
type noReleaseSandbox struct{ goatherdsandbox.Sandbox }

func TestRetainedReport_ContainerSessionRequiresRelease(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	sessionOpen, _ := cleanOpener()
	session, err := StartSessionSandbox(context.Background(), sessionTestConfig(), sessionOpen, "", nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	session.sb = noReleaseSandbox{session.sb}

	dispatchOpen, _ := cleanOpener()
	rt := containerReportRuntime(t, fx, repo, rootID, t.TempDir(), func(context.Context, string, SubAgentParams) (string, error) {
		t.Fatal("a backend that cannot release a capture must not run")
		return "", nil
	}, "auto", dispatchOpen)
	rt.DispatchWorlds.SessionSandbox = session
	rt.World = "container"

	if _, err := startRetainedRun(context.Background(), rt, "inspect", SubAgentParams{Role: "reviewer"}, 0, true); err == nil {
		t.Fatal("a capturing backend without ReleaseState must fail closed")
	} else if !strings.Contains(err.Error(), "ReleaseState") {
		t.Fatalf("error = %q, want the release contract named", err)
	}
}
