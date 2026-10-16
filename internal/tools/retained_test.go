package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// decodeProposalEnvelope unmarshals a proposal envelope and fails the test on
// invalid JSON.
func decodeProposalEnvelope(t *testing.T, result string) proposalEnvelope {
	t.Helper()
	var env proposalEnvelope
	if err := json.Unmarshal([]byte(result), &env); err != nil {
		t.Fatalf("proposal envelope is not JSON: %v\n%s", err, result)
	}
	return env
}

// newRetainedTestEnv builds a RetainedRuntime over a real temp repo, a real
// scope manager, and a worktree root, with the given runner.
func newRetainedTestEnv(t *testing.T, runner *sessionRunner) RetainedRuntime {
	t.Helper()
	fx, repo, rootID := newSessionFixtures(t)
	return RetainedRuntime{
		Runner:       runner.run(),
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
	}
}

// writingRunner returns a runner whose sub-agent writes name in its worktree.
func writingRunner(t *testing.T, name string) *sessionRunner {
	t.Helper()
	r := &sessionRunner{}
	r.sideEffect = func(call int, _ string) {
		writeTestFile(t, r.workdirFor(t, call), name, "from sub-agent\n")
	}
	return r
}

// TestRetained_ReturnsProposal: a retained dispatch runs the sub-agent in an
// isolated worktree and returns a sealed proposal — not a host mutation.
func TestRetained_ReturnsProposal(t *testing.T) {
	resetRetainedProposals()
	runner := writingRunner(t, "out.txt")
	rt := newRetainedTestEnv(t, runner)

	res, err := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "worker"}, 0)
	if err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	env := decodeProposalEnvelope(t, res)
	if env.Status != "proposal" || env.ProposalID == "" {
		t.Fatalf("want a sealed proposal, got %+v", env)
	}
	if len(env.Files) == 0 || !strings.Contains(strings.Join(env.Files, ","), "out.txt") {
		t.Errorf("proposal should list the changed file, got %v", env.Files)
	}

	wd := runner.workdirFor(t, 0)
	if wd == "" || wd == rt.RepoPath {
		t.Fatalf("the dispatch must run in an isolated worktree, got workdir %q", wd)
	}
	if _, err := os.Stat(filepath.Join(wd, "out.txt")); err != nil {
		t.Errorf("the work should be in the worktree: %v", err)
	}
	// And never on the host until settled.
	if _, err := os.Stat(filepath.Join(rt.RepoPath, "out.txt")); !os.IsNotExist(err) {
		t.Errorf("the host tree must be untouched before settlement (stat err=%v)", err)
	}
}

// TestRetained_HostUntouchedUntilSettle: the host tree changes only when the
// proposal is settled — select lands the work.
func TestRetained_HostUntouchedUntilSettle(t *testing.T) {
	resetRetainedProposals()
	rt := newRetainedTestEnv(t, writingRunner(t, "out.txt"))

	res, err := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "worker"}, 0)
	if err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	env := decodeProposalEnvelope(t, res)

	if _, err := os.Stat(filepath.Join(rt.RepoPath, "out.txt")); !os.IsNotExist(err) {
		t.Fatalf("the host tree must be untouched before settlement (stat err=%v)", err)
	}

	settled, err := settleRetainedProposal(context.Background(), env.ProposalID, "select")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got := decodeProposalEnvelope(t, settled); got.Status != "settled" || got.Action != "select" {
		t.Fatalf("settle envelope = %+v, want settled/select", got)
	}
	if _, err := os.Stat(filepath.Join(rt.RepoPath, "out.txt")); err != nil {
		t.Fatalf("select must land the work on the host: %v", err)
	}
}

// TestRetained_DiscardLeavesHostUntouched: discard throws the proposal away and
// the host never sees it.
func TestRetained_DiscardLeavesHostUntouched(t *testing.T) {
	resetRetainedProposals()
	rt := newRetainedTestEnv(t, writingRunner(t, "out.txt"))

	res, _ := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "worker"}, 0)
	env := decodeProposalEnvelope(t, res)

	if _, err := settleRetainedProposal(context.Background(), env.ProposalID, "discard"); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rt.RepoPath, "out.txt")); !os.IsNotExist(err) {
		t.Fatalf("discard must leave the host untouched (stat err=%v)", err)
	}
}

// TestRetained_ReleaseRemovesWorktree: release integrates nothing, so it must
// tear the isolated worktree down rather than strand it, and must not touch the
// host.
func TestRetained_ReleaseRemovesWorktree(t *testing.T) {
	resetRetainedProposals()
	runner := writingRunner(t, "out.txt")
	rt := newRetainedTestEnv(t, runner)

	res, _ := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "worker"}, 0)
	env := decodeProposalEnvelope(t, res)
	wt := runner.workdirFor(t, 0)

	if _, err := settleRetainedProposal(context.Background(), env.ProposalID, "release"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("release must tear down the worktree (stat err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(rt.RepoPath, "out.txt")); !os.IsNotExist(err) {
		t.Errorf("release must not touch the host (stat err=%v)", err)
	}
}

// TestRetained_ParallelIDsUnique: proposals dispatched in the same nanosecond
// get distinct ids — and therefore distinct worktree paths.
func TestRetained_ParallelIDsUnique(t *testing.T) {
	resetRetainedProposals()
	rt := newRetainedTestEnv(t, writingRunner(t, "out.txt"))

	r1, _ := startRetainedRun(context.Background(), rt, "one", SubAgentParams{Role: "worker"}, 0)
	r2, _ := startRetainedRun(context.Background(), rt, "two", SubAgentParams{Role: "worker"}, 0)
	e1, e2 := decodeProposalEnvelope(t, r1), decodeProposalEnvelope(t, r2)
	if e1.ProposalID == "" || e2.ProposalID == "" {
		t.Fatalf("both dispatches should seal a proposal: %v / %v", e1, e2)
	}
	if e1.ProposalID == e2.ProposalID {
		t.Fatalf("parallel dispatches must not collide on a proposal id: %q", e1.ProposalID)
	}
}

// TestRetained_SettleOnce: a sealed proposal settles exactly once.
func TestRetained_SettleOnce(t *testing.T) {
	resetRetainedProposals()
	rt := newRetainedTestEnv(t, writingRunner(t, "out.txt"))

	res, _ := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "worker"}, 0)
	env := decodeProposalEnvelope(t, res)

	if _, err := settleRetainedProposal(context.Background(), env.ProposalID, "select"); err != nil {
		t.Fatalf("first settle: %v", err)
	}
	if _, err := settleRetainedProposal(context.Background(), env.ProposalID, "discard"); err == nil {
		t.Fatal("a second settlement must be refused (the proposal is consumed)")
	}
}

// TestProposalTool_Surface: the proposal tool lists, shows, and discards.
func TestProposalTool_Surface(t *testing.T) {
	resetRetainedProposals()
	rt := newRetainedTestEnv(t, writingRunner(t, "out.txt"))
	res, _ := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "worker"}, 0)
	env := decodeProposalEnvelope(t, res)

	tool := &ProposalTool{}
	if tool.Name() != "proposal" {
		t.Errorf("tool name = %q, want proposal", tool.Name())
	}

	out, err := tool.Execute(context.Background(), `{"action":"list"}`)
	if err != nil || !strings.Contains(out, env.ProposalID) {
		t.Fatalf("list should name the open proposal: %q err=%v", out, err)
	}
	out, err = tool.Execute(context.Background(), `{"action":"show","proposal_id":`+jsonString(env.ProposalID)+`}`)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if decodeProposalEnvelope(t, out).ProposalID != env.ProposalID {
		t.Errorf("show returned the wrong proposal: %s", out)
	}
	if _, err := tool.Execute(context.Background(), `{"action":"discard","proposal_id":`+jsonString(env.ProposalID)+`}`); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if _, err := tool.Execute(context.Background(), `{"action":"select"}`); err == nil {
		t.Error("select without a proposal_id must error")
	}
}
