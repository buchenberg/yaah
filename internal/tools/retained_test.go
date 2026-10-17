package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// TestRetained_SpeculationSettlesOneAndDiscardsTheRest pins the mechanics the
// speculation guidance relies on: N dispatches of one task, issued together,
// each get their own world and seal their own proposal; the orchestrator compares
// them, settles one, and discards the rest.
//
// There is deliberately no `attempts` parameter on spawn_subagent: the loop
// already fans out parallel calls in one turn (each takes a sub-agent slot), so a
// tool-level multiplier would duplicate that and bypass the concurrency cap.
func TestRetained_SpeculationSettlesOneAndDiscardsTheRest(t *testing.T) {
	resetRetainedProposals()

	const attempts = 3
	runner := &sessionRunner{responses: []runnerResponse{
		{result: "approach a"}, {result: "approach b"}, {result: "approach c"},
	}}
	// Everything touched from a dispatch goroutine is non-fatal: t.Fatalf is
	// only legal on the test's own goroutine, and a failure there would surface
	// later as a misleading assertion.
	writeErrs := make([]error, attempts)
	runner.sideEffect = func(call int, _ string) {
		wd, ok := runner.workdirAt(call)
		if !ok {
			writeErrs[call] = fmt.Errorf("no workdir recorded for call %d", call)
			return
		}
		writeErrs[call] = os.WriteFile(filepath.Join(wd, fmt.Sprintf("approach-%d.txt", call)), []byte("variant\n"), 0o644)
	}
	rt := newRetainedTestEnv(t, runner)

	var wg sync.WaitGroup
	envs := make([]proposalEnvelope, attempts)
	runErrs := make([]error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := startRetainedRun(context.Background(), rt, "propose a fix", SubAgentParams{Role: "worker"}, 0)
			if err != nil {
				runErrs[i] = err
				return
			}
			var env proposalEnvelope
			if uerr := json.Unmarshal([]byte(res), &env); uerr != nil {
				runErrs[i] = fmt.Errorf("envelope: %w", uerr)
				return
			}
			envs[i] = env
		}(i)
	}
	wg.Wait()
	for i, err := range runErrs {
		if err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
	}
	for call, err := range writeErrs {
		if err != nil {
			t.Fatalf("attempt %d's write: %v", call, err)
		}
	}

	seenID := map[string]bool{}
	for i, env := range envs {
		if env.Status != "proposal" || env.ProposalID == "" {
			t.Fatalf("attempt %d did not seal a proposal: %+v", i, env)
		}
		if seenID[env.ProposalID] {
			t.Fatalf("two attempts share a proposal id: %q", env.ProposalID)
		}
		seenID[env.ProposalID] = true
	}

	// Each attempt ran in its own world, not in the others'.
	workdirs := map[string]bool{}
	for call := 0; call < attempts; call++ {
		wd := runner.workdirFor(t, call)
		if wd == rt.RepoPath || wd == "" {
			t.Fatalf("attempt %d is not isolated: %q", call, wd)
		}
		if workdirs[wd] {
			t.Fatalf("two attempts share a world: %q", wd)
		}
		workdirs[wd] = true
	}

	// The orchestrator picks one and throws the others away. A goroutine's index
	// is not the runner's call index (they are concurrent), so the attempt is
	// identified by the report it returned.
	callOf := map[string]int{}
	for k := 0; k < attempts; k++ {
		callOf[runner.responses[k].result] = k
	}
	const winner = 1 // the call that returned "approach b"
	for _, env := range envs {
		call, ok := callOf[env.Result]
		if !ok {
			t.Fatalf("proposal result %q matches no attempt", env.Result)
		}
		if call == winner {
			if _, err := settleRetainedProposal(context.Background(), env.ProposalID, "select"); err != nil {
				t.Fatalf("select the winner: %v", err)
			}
			continue
		}
		if _, err := settleRetainedProposal(context.Background(), env.ProposalID, "discard"); err != nil {
			t.Fatalf("discard attempt %d: %v", call, err)
		}
	}

	// Only the winner's change landed, and the losers' worlds are gone.
	for call := 0; call < attempts; call++ {
		_, err := os.Stat(filepath.Join(rt.RepoPath, fmt.Sprintf("approach-%d.txt", call)))
		if call == winner {
			if err != nil {
				t.Errorf("the winner's change must land: %v", err)
			}
			continue
		}
		if !os.IsNotExist(err) {
			t.Errorf("attempt %d must not land (stat err=%v)", call, err)
		}
		if _, werr := os.Stat(runner.workdirFor(t, call)); !os.IsNotExist(werr) {
			t.Errorf("discard must remove attempt %d's worktree (stat err=%v)", call, werr)
		}
	}

	for _, env := range envs {
		if _, err := getRetainedProposal(env.ProposalID); err == nil {
			t.Errorf("proposal %s should be consumed", env.ProposalID)
		}
	}
}
