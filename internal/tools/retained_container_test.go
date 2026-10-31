package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
	"github.com/buchenberg/yaah/internal/agent/subagent"
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

	var gotWorld SubAgentWorld
	ctx := WithSubAgentWorldPtr(context.Background(), &gotWorld)

	res, err := startRetainedRun(ctx, rt, "inspect", SubAgentParams{Role: "reviewer"}, 0, true)
	if err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	if res != "saw the session state" {
		t.Fatalf("result = %q", res)
	}
	if gotWorld.Resolved != string(subagent.WorldContainer) || gotWorld.Degraded {
		t.Errorf("sibling world = %+v, want container and not degraded", gotWorld)
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

	var gotWorld SubAgentWorld
	ctx := WithSubAgentWorldPtr(context.Background(), &gotWorld)

	res, err := startRetainedRun(ctx, rt, "inspect", SubAgentParams{Role: "reviewer"}, 0, true)
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
	if gotWorld.Resolved != string(subagent.WorldShared) || !gotWorld.Degraded || gotWorld.Reason == "" {
		t.Errorf("fallback world = %+v, want shared and degraded with a reason", gotWorld)
	}
}

// TestRetainedReport_ContainerSessionBrokerFallbackRefused pins requirement-
// preserving degradation for a broker posture: the session container has no
// broker allowlist, so a sibling preflight failure fails the report instead of
// running with egress the declaration did not grant.
func TestRetainedReport_ContainerSessionBrokerFallbackRefused(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	sessionOpen, _ := cleanOpener()
	session, err := StartSessionSandbox(context.Background(), sessionTestConfig(), sessionOpen, "", nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox(session): %v", err)
	}
	var dispatched bool
	run := func(context.Context, string, SubAgentParams) (string, error) {
		dispatched = true
		return "preflight fallback report", nil
	}
	openFail := func(config.WorkspaceConfig) (goatherdsandbox.Sandbox, error) {
		return nil, errors.New("broker unavailable")
	}
	rt := containerReportRuntime(t, fx, repo, rootID, t.TempDir(), run, "auto", openFail)
	rt.DispatchWorlds.SessionSandbox = session
	rt.Network = "broker"
	rt.NetworkHosts = []string{"api.example.com"}

	if _, err := startRetainedRun(context.Background(), rt, "inspect", SubAgentParams{Role: "reviewer"}, 0, true); err == nil {
		t.Fatal("a broker-shaped report must fail when only the session container remains")
	} else if !strings.Contains(err.Error(), "cannot preserve") || !strings.Contains(err.Error(), "broker unavailable") {
		t.Fatalf("error = %v, want the authority downgrade and preflight cause", err)
	}
	if dispatched {
		t.Fatal("the report body must not run after the fallback is refused")
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

// TestRetainedReport_ContainerWorldLowersAuthority pins the grant and network
// lowering at the opener seam: the final shaped sandbox receives the container
// roots, broker posture, and allowlist, while the bootstrap staging sandbox
// stays unshaped and loopback-only.
func TestRetainedReport_ContainerWorldLowersAuthority(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	base, fakes := cleanOpener()
	var configs []config.WorkspaceConfig
	open := func(w config.WorkspaceConfig) (goatherdsandbox.Sandbox, error) {
		configs = append(configs, w)
		return base(w)
	}
	run := func(context.Context, string, SubAgentParams) (string, error) {
		return "shaped report", nil
	}
	rt := containerReportRuntime(t, fx, repo, rootID, t.TempDir(), run, "auto", open)
	rt.Grants = []config.GrantConfig{{Path: "src", Access: "readwrite"}, {Path: "docs", Access: "readonly"}}
	rt.Network = "broker"
	rt.NetworkHosts = []string{"api.example.com"}

	res, err := startRetainedRun(context.Background(), rt, "inspect", SubAgentParams{Role: "reviewer"}, 0, true)
	if err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	if res != "shaped report" {
		t.Fatalf("result = %q", res)
	}
	if len(configs) != 4 {
		t.Fatalf("opener calls = %d, want 4 (staging probe/session, shaped probe/session)", len(configs))
	}
	staging := configs[1]
	if len(staging.WritableRoots) != 0 || staging.MaskWorkspace || staging.Network != "none" || len(staging.BrokerHosts) != 0 {
		t.Errorf("staging config must stay unshaped and loopback-only: %+v", staging)
	}
	shaped := configs[3]
	if !reflect.DeepEqual(shaped.WritableRoots, []string{"/workspace/src"}) {
		t.Errorf("shaped writable roots = %v, want [/workspace/src]", shaped.WritableRoots)
	}
	if shaped.MaskWorkspace {
		t.Error("a writable grant must not set MaskWorkspace")
	}
	if shaped.Network != "broker" || !reflect.DeepEqual(shaped.BrokerHosts, []string{"api.example.com"}) {
		t.Errorf("shaped network = %q hosts = %v", shaped.Network, shaped.BrokerHosts)
	}
	for i, sb := range fakes() {
		if !sb.Destroyed() {
			t.Errorf("sandbox %d must be destroyed", i)
		}
	}
}

// TestRetainedReport_ContainerWorldReadOnlyMask pins the all-read-only grant
// case: the shaped sandbox has no writable root but is explicitly masked, which
// is what distinguishes it from the unshaped default.
func TestRetainedReport_ContainerWorldReadOnlyMask(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	base, _ := cleanOpener()
	var configs []config.WorkspaceConfig
	open := func(w config.WorkspaceConfig) (goatherdsandbox.Sandbox, error) {
		configs = append(configs, w)
		return base(w)
	}
	run := func(context.Context, string, SubAgentParams) (string, error) {
		return "read-only report", nil
	}
	rt := containerReportRuntime(t, fx, repo, rootID, t.TempDir(), run, "auto", open)
	rt.Grants = []config.GrantConfig{{Path: "docs", Access: "readonly"}}

	if _, err := startRetainedRun(context.Background(), rt, "inspect", SubAgentParams{Role: "reviewer"}, 0, true); err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	if len(configs) != 4 {
		t.Fatalf("opener calls = %d, want 4", len(configs))
	}
	shaped := configs[3]
	if len(shaped.WritableRoots) != 0 || !shaped.MaskWorkspace {
		t.Errorf("all-read-only grants must set the mask with no roots: %+v", shaped)
	}
}

// TestRetainedReport_ContainerWorldNetworkOnlyAvoidsStaging pins the other
// half: a network posture without grants does not need mount shaping, so the
// copy-in runs directly in the final sandbox.
func TestRetainedReport_ContainerWorldNetworkOnlyAvoidsStaging(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	base, _ := cleanOpener()
	var configs []config.WorkspaceConfig
	open := func(w config.WorkspaceConfig) (goatherdsandbox.Sandbox, error) {
		configs = append(configs, w)
		return base(w)
	}
	run := func(context.Context, string, SubAgentParams) (string, error) {
		return "network-only report", nil
	}
	rt := containerReportRuntime(t, fx, repo, rootID, t.TempDir(), run, "auto", open)
	rt.Network = "deny_all"

	if _, err := startRetainedRun(context.Background(), rt, "inspect", SubAgentParams{Role: "reviewer"}, 0, true); err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("opener calls = %d, want 2 (probe then dispatch sandbox)", len(configs))
	}
	if configs[1].Network != "none" || configs[1].MaskWorkspace || len(configs[1].WritableRoots) != 0 {
		t.Errorf("network-only config = %+v", configs[1])
	}
}

// TestRetainedProposal_ContainerWorldShapedGrants pins the proposal half of the
// lowered authority: the body writes inside a granted root, the shaped sandbox
// has that root as its only writable subtree, and the snapshot-native seal
// produces a proposal against the applied staging baseline.
func TestRetainedProposal_ContainerWorldShapedGrants(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	base, _ := cleanOpener()
	var configs []config.WorkspaceConfig
	open := func(w config.WorkspaceConfig) (goatherdsandbox.Sandbox, error) {
		configs = append(configs, w)
		return base(w)
	}
	run := func(ctx context.Context, _ string, _ SubAgentParams) (string, error) {
		ws := DispatchWorkspace(ctx)
		if ws == nil {
			return "", errors.New("proposal body has no dispatch workspace")
		}
		if err := ws.WriteFile(ctx, "src/new.txt", []byte("hello\n"), 0o644); err != nil {
			return "", err
		}
		return "wrote a granted file", nil
	}
	rt := containerReportRuntime(t, fx, repo, rootID, t.TempDir(), run, "auto", open)
	rt.Grants = []config.GrantConfig{{Path: "src", Access: "readwrite"}}
	rt.Network = "deny_all"

	out, err := startRetainedRun(context.Background(), rt, "make a change", SubAgentParams{Role: "developer"}, 0, false)
	if err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	if !strings.Contains(out, `"status":"proposal"`) || !strings.Contains(out, "src/new.txt") {
		t.Fatalf("proposal envelope = %s", out)
	}
	if len(configs) != 4 {
		t.Fatalf("opener calls = %d, want 4", len(configs))
	}
	shaped := configs[3]
	if !reflect.DeepEqual(shaped.WritableRoots, []string{"/workspace/src"}) || shaped.Network != "none" {
		t.Fatalf("shaped proposal config = %+v", shaped)
	}
}

// TestRetainedReport_ContainerSessionSiblingLowersAuthority pins the sibling
// path: a container-session dispatch is shaped from the same grants and broker
// posture, but is seeded by applying the session capture rather than a host
// copy-in, so no staging container is needed.
func TestRetainedReport_ContainerSessionSiblingLowersAuthority(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	sessionOpen, _ := cleanOpener()
	session, err := StartSessionSandbox(context.Background(), sessionTestConfig(), sessionOpen, "", nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox(session): %v", err)
	}
	base, _ := cleanOpener()
	var configs []config.WorkspaceConfig
	open := func(w config.WorkspaceConfig) (goatherdsandbox.Sandbox, error) {
		configs = append(configs, w)
		return base(w)
	}
	run := func(context.Context, string, SubAgentParams) (string, error) {
		return "sibling report", nil
	}
	rt := containerReportRuntime(t, fx, repo, rootID, t.TempDir(), run, "auto", open)
	rt.DispatchWorlds.SessionSandbox = session
	rt.Grants = []config.GrantConfig{{Path: "src", Access: "readwrite"}}
	rt.Network = "broker"
	rt.NetworkHosts = []string{"api.example.com"}

	if _, err := startRetainedRun(context.Background(), rt, "inspect", SubAgentParams{Role: "reviewer"}, 0, true); err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("opener calls = %d, want 2 (probe then sibling)", len(configs))
	}
	shaped := configs[1]
	if !reflect.DeepEqual(shaped.WritableRoots, []string{"/workspace/src"}) || shaped.Network != "broker" || !reflect.DeepEqual(shaped.BrokerHosts, []string{"api.example.com"}) {
		t.Fatalf("sibling config = %+v", shaped)
	}
}

// TestRetainedReport_ContainerSessionGrantFallbackRefused pins requirement-
// preserving degradation: the session container is not shaped, so it cannot
// enforce a sibling's grants. A preflight failure must fail the report rather
// than run the body with wider write authority than declared.
func TestRetainedReport_ContainerSessionGrantFallbackRefused(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	sessionOpen, _ := cleanOpener()
	session, err := StartSessionSandbox(context.Background(), sessionTestConfig(), sessionOpen, "", nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox(session): %v", err)
	}
	run := func(context.Context, string, SubAgentParams) (string, error) {
		t.Fatal("a grant-shaped dispatch must not fall back to the unshaped session container")
		return "", nil
	}
	openFail := func(config.WorkspaceConfig) (goatherdsandbox.Sandbox, error) {
		return nil, errors.New("daemon unreachable")
	}
	rt := containerReportRuntime(t, fx, repo, rootID, t.TempDir(), run, "auto", openFail)
	rt.DispatchWorlds.SessionSandbox = session
	rt.Grants = []config.GrantConfig{{Path: "src", Access: "readwrite"}}
	rt.Network = "deny_all"

	if _, err := startRetainedRun(context.Background(), rt, "inspect", SubAgentParams{Role: "reviewer"}, 0, true); err == nil {
		t.Fatal("a grant-shaped report must fail when only the unshaped session container remains")
	} else if !strings.Contains(err.Error(), "cannot preserve") {
		t.Fatalf("error = %v, want it to name the authority downgrade", err)
	}
}

// TestRetainedReport_ContainerSessionDenyAllFallbackStillRuns pins the other
// side: deny_all is preserved by the session container's loopback-only
// namespace, so that fallback remains legal.
func TestRetainedReport_ContainerSessionDenyAllFallbackStillRuns(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	sessionOpen, _ := cleanOpener()
	session, err := StartSessionSandbox(context.Background(), sessionTestConfig(), sessionOpen, "", nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox(session): %v", err)
	}
	run := func(context.Context, string, SubAgentParams) (string, error) {
		return "session fallback with deny_all", nil
	}
	openFail := func(config.WorkspaceConfig) (goatherdsandbox.Sandbox, error) {
		return nil, errors.New("daemon unreachable")
	}
	rt := containerReportRuntime(t, fx, repo, rootID, t.TempDir(), run, "auto", openFail)
	rt.DispatchWorlds.SessionSandbox = session
	rt.Network = "deny_all"

	res, err := startRetainedRun(context.Background(), rt, "inspect", SubAgentParams{Role: "reviewer"}, 0, true)
	if err != nil {
		t.Fatalf("deny_all fallback must preserve the posture: %v", err)
	}
	if !strings.Contains(res, "session fallback with deny_all") {
		t.Fatalf("result = %q", res)
	}
}
