package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherd "github.com/buchenberg/goatherd-runtime"
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
		// A registry per test, as the composition root injects one per
		// session (yaah-yox).
		Proposals: NewProposalRegistry(),
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

// TestRetained_TimeoutNamesRoleBudget: a retained run that exhausts its
// wall-clock budget must not report a provider error — the provider was only
// in flight when the role timeout expired.
func TestRetained_TimeoutNamesRoleBudget(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)

	rt := RetainedRuntime{
		Runner: func(ctx context.Context, _ string, _ SubAgentParams) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}

	res, err := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "developer"}, 50*time.Millisecond, false)
	if err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	env := decodeProposalEnvelope(t, res)
	for _, want := range []string{"sub-agent timed out", "role developer", "default_timeout"} {
		if !strings.Contains(env.Error, want) {
			t.Errorf("timeout error should mention %q, got %q", want, env.Error)
		}
	}
	if strings.Contains(env.Error, "provider error") {
		t.Errorf("a role-budget timeout must not be attributed to the provider: %q", env.Error)
	}
}

func TestRetainedBodyFailure_ParentDeadlineWithoutBudget(t *testing.T) {
	got := retainedBodyFailure(context.DeadlineExceeded, "developer", 0, retainedFailureParent)
	if !strings.Contains(got, "parent context deadline") {
		t.Errorf("message = %q, want a parent-deadline explanation", got)
	}
	if strings.Contains(got, "raise the role timeout") {
		t.Errorf("message = %q, must not blame the role timeout when none is configured", got)
	}
}

func TestRetainedBodyFailure_ProviderDeadline(t *testing.T) {
	got := retainedBodyFailure(context.DeadlineExceeded, "developer", 50*time.Millisecond, retainedFailureProvider)
	if strings.Contains(got, "raise the role timeout") || strings.Contains(got, "sub-agent timed out") {
		t.Errorf("message = %q, must preserve a provider-internal deadline", got)
	}
}

func TestClassifyRetainedBodyFailure(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		timeout       time.Duration
		runErr        error
		parentErr     error
		budgetGoverns bool
		want          retainedFailureSource
	}{
		{"provider deadline while contexts live", context.DeadlineExceeded, 50 * time.Millisecond, nil, nil, true, retainedFailureProvider},
		{"dispatch budget", context.DeadlineExceeded, 50 * time.Millisecond, context.DeadlineExceeded, nil, true, retainedFailureBudget},
		{"parent deadline first", context.DeadlineExceeded, 50 * time.Millisecond, context.DeadlineExceeded, context.DeadlineExceeded, false, retainedFailureParent},
		{"parent deadline without budget", context.DeadlineExceeded, 0, context.DeadlineExceeded, context.DeadlineExceeded, false, retainedFailureParent},
		{"cancellation", context.Canceled, 50 * time.Millisecond, context.Canceled, nil, true, retainedFailureProvider},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyRetainedBodyFailure(tc.err, tc.timeout, tc.runErr, tc.parentErr, tc.budgetGoverns); got != tc.want {
				t.Errorf("source = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRetained_ReturnsProposal: a retained dispatch runs the sub-agent in an
// isolated worktree and returns a sealed proposal — not a host mutation.
func TestRetained_ReturnsProposal(t *testing.T) {
	runner := writingRunner(t, "out.txt")
	rt := newRetainedTestEnv(t, runner)

	res, err := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "worker"}, 50*time.Millisecond, false)
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
	rt := newRetainedTestEnv(t, writingRunner(t, "out.txt"))

	res, err := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "worker"}, 50*time.Millisecond, false)
	if err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	env := decodeProposalEnvelope(t, res)

	if _, err := os.Stat(filepath.Join(rt.RepoPath, "out.txt")); !os.IsNotExist(err) {
		t.Fatalf("the host tree must be untouched before settlement (stat err=%v)", err)
	}

	settled, err := rt.Proposals.settleRetainedProposal(context.Background(), env.ProposalID, "select")
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
	rt := newRetainedTestEnv(t, writingRunner(t, "out.txt"))

	res, _ := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "worker"}, 0, false)
	env := decodeProposalEnvelope(t, res)

	if _, err := rt.Proposals.settleRetainedProposal(context.Background(), env.ProposalID, "discard"); err != nil {
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
	runner := writingRunner(t, "out.txt")
	rt := newRetainedTestEnv(t, runner)

	res, _ := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "worker"}, 0, false)
	env := decodeProposalEnvelope(t, res)
	wt := runner.workdirFor(t, 0)

	settled, err := rt.Proposals.settleRetainedProposal(context.Background(), env.ProposalID, "release")
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("release must tear down the worktree (stat err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(rt.RepoPath, "out.txt")); !os.IsNotExist(err) {
		t.Errorf("release must not touch the host (stat err=%v)", err)
	}

	// yaah-0e4: release must report where the sealed delta went — and it must
	// actually exist there — before the worktree is destroyed.
	senv := decodeProposalEnvelope(t, settled)
	if senv.Retained == "" {
		t.Fatalf("release envelope must carry a retained path, got %#v", senv)
	}
	if data, err := os.ReadFile(senv.Retained); err != nil {
		t.Errorf("retained delta not readable at %s: %v", senv.Retained, err)
	} else if string(data) == "" {
		t.Errorf("retained delta at %s is empty", senv.Retained)
	}
}

// TestRetained_ReleaseTeardownFailureSurfaced pins yaah-wpf: when the child
// scope cannot be torn down after a release, the failure must reach the
// envelope's Notes — with the stranded worktree path — instead of being
// swallowed while the tool claims a clean settlement.
func TestRetained_ReleaseTeardownFailureSurfaced(t *testing.T) {
	runner := writingRunner(t, "out.txt")
	rt := newRetainedTestEnv(t, runner)

	res, _ := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "worker"}, 0, false)
	env := decodeProposalEnvelope(t, res)

	// Discard the child scope out from under the proposal so the release
	// teardown's parent.Discard(child) deterministically fails.
	p, perr := rt.Proposals.get(env.ProposalID)
	if perr != nil {
		t.Fatalf("get proposal: %v", perr)
	}
	parent, ok := p.mgr.Get(p.parentID)
	if !ok {
		t.Fatalf("parent scope %s not found", p.parentID)
	}
	child, ok := p.mgr.Get(p.scopeID)
	if !ok {
		t.Fatalf("child scope %s not found", p.scopeID)
	}
	if err := parent.Discard(child); err != nil {
		t.Fatalf("pre-discard: %v", err)
	}

	settled, err := rt.Proposals.settleRetainedProposal(context.Background(), env.ProposalID, "release")
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	senv := decodeProposalEnvelope(t, settled)
	if senv.Notes == "" {
		t.Fatalf("teardown failure must surface in Notes, got %#v", senv)
	}
	if senv.Status != "settled" {
		t.Errorf("release itself settled; status = %q, want settled", senv.Status)
	}
}

// TestRetained_ParallelIDsUnique: proposals dispatched in the same nanosecond
// get distinct ids — and therefore distinct worktree paths.
func TestRetained_ParallelIDsUnique(t *testing.T) {
	rt := newRetainedTestEnv(t, writingRunner(t, "out.txt"))

	r1, _ := startRetainedRun(context.Background(), rt, "one", SubAgentParams{Role: "worker"}, 0, false)
	r2, _ := startRetainedRun(context.Background(), rt, "two", SubAgentParams{Role: "worker"}, 0, false)
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
	rt := newRetainedTestEnv(t, writingRunner(t, "out.txt"))

	res, _ := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "worker"}, 0, false)
	env := decodeProposalEnvelope(t, res)

	if _, err := rt.Proposals.settleRetainedProposal(context.Background(), env.ProposalID, "select"); err != nil {
		t.Fatalf("first settle: %v", err)
	}
	if _, err := rt.Proposals.settleRetainedProposal(context.Background(), env.ProposalID, "discard"); err == nil {
		t.Fatal("a second settlement must be refused (the proposal is consumed)")
	}
}

// TestProposalTool_Surface: the proposal tool lists, shows, and discards.
func TestProposalTool_Surface(t *testing.T) {
	rt := newRetainedTestEnv(t, writingRunner(t, "out.txt"))
	res, _ := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "worker"}, 0, false)
	env := decodeProposalEnvelope(t, res)

	tool := &ProposalTool{Proposals: rt.Proposals}
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
// There is deliberately no `attempts` parameter on delegate: the loop
// already fans out parallel calls in one turn (each takes a sub-agent slot), so a
// tool-level multiplier would duplicate that and bypass the concurrency cap.
func TestRetained_SpeculationSettlesOneAndDiscardsTheRest(t *testing.T) {

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
		// Each attempt must write distinct bytes: identical content would hash
		// to one blob, and the concurrent `git add -A` that seals each attempt
		// would race on creating that same loose object. POSIX tolerates
		// renaming over an existing object; Git for Windows fails with
		// "unable to write file .git/objects/...: Permission denied".
		writeErrs[call] = os.WriteFile(filepath.Join(wd, fmt.Sprintf("approach-%d.txt", call)), []byte(fmt.Sprintf("variant %d\n", call)), 0o644)
	}
	rt := newRetainedTestEnv(t, runner)

	var wg sync.WaitGroup
	envs := make([]proposalEnvelope, attempts)
	runErrs := make([]error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := startRetainedRun(context.Background(), rt, "propose a fix", SubAgentParams{Role: "worker"}, 0, false)
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
			if _, err := rt.Proposals.settleRetainedProposal(context.Background(), env.ProposalID, "select"); err != nil {
				t.Fatalf("select the winner: %v", err)
			}
			continue
		}
		if _, err := rt.Proposals.settleRetainedProposal(context.Background(), env.ProposalID, "discard"); err != nil {
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
		if _, err := rt.Proposals.get(env.ProposalID); err == nil {
			t.Errorf("proposal %s should be consumed", env.ProposalID)
		}
	}
}

// TestRetained_PartialOutputSurvivesBodyError: a run that stops early still
// seals its partial work. The envelope must carry both the partial result and
// the reason it stopped, or the orchestrator reads truncated work as finished.
func TestRetained_PartialOutputSurvivesBodyError(t *testing.T) {
	r := &sessionRunner{responses: []runnerResponse{{
		result:         "almost done",
		waitForContext: true,
	}}}
	r.sideEffect = func(call int, _ string) {
		writeTestFile(t, r.workdirFor(t, call), "partial.txt", "work in progress\n")
	}
	rt := newRetainedTestEnv(t, r)

	res, err := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "worker"}, 50*time.Millisecond, false)
	if err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	env := decodeProposalEnvelope(t, res)
	if env.Status != "proposal" {
		t.Fatalf("status = %q, want proposal (partial work is still reviewable)", env.Status)
	}
	if !strings.Contains(env.Result, "almost done") {
		t.Errorf("result = %q, want the partial output", env.Result)
	}
	if !strings.Contains(env.Error, "sub-agent timed out") || !strings.Contains(env.Error, "role worker") {
		t.Errorf("error = %q, want the role-budget reason the run stopped", env.Error)
	}
}

// TestProposalTool_ListEnvelope pins yaah-yox's list shape: `list` answers with
// the same envelope the other verbs do, and an empty registry still emits an
// explicit `proposals: []` — "nothing is open" must be a parsed fact, not prose
// a caller cannot tell from a failed parse.
func TestProposalTool_ListEnvelope(t *testing.T) {
	t.Run("empty registry emits an explicit empty list", func(t *testing.T) {
		tool := &ProposalTool{Proposals: NewProposalRegistry()}
		out, err := tool.Execute(context.Background(), `{"action":"list"}`)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		var fields map[string]json.RawMessage
		if uerr := json.Unmarshal([]byte(out), &fields); uerr != nil {
			t.Fatalf("list result is not JSON: %v\n%s", uerr, out)
		}
		if _, ok := fields["status"]; !ok {
			t.Errorf("list result must carry a status, got %s", out)
		}
		raw, ok := fields["proposals"]
		if !ok {
			t.Fatalf("list result must always carry a proposals key, got %s", out)
		}
		if got := strings.TrimSpace(string(raw)); got != "[]" {
			t.Errorf("empty registry must list proposals as [], got %s", got)
		}
	})

	t.Run("open proposals are listed with their files", func(t *testing.T) {
		rt := newRetainedTestEnv(t, writingRunner(t, "listed.txt"))
		res, err := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "worker"}, 0, false)
		if err != nil {
			t.Fatalf("startRetainedRun: %v", err)
		}
		env := decodeProposalEnvelope(t, res)

		out, err := (&ProposalTool{Proposals: rt.Proposals}).Execute(context.Background(), `{"action":"list"}`)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		var listed struct {
			Status    string            `json:"status"`
			Proposals []proposalSummary `json:"proposals"`
		}
		if uerr := json.Unmarshal([]byte(out), &listed); uerr != nil {
			t.Fatalf("list result is not JSON: %v\n%s", uerr, out)
		}
		if len(listed.Proposals) != 1 {
			t.Fatalf("want exactly the one open proposal, got %#v", listed.Proposals)
		}
		got := listed.Proposals[0]
		if got.ProposalID != env.ProposalID {
			t.Errorf("proposal_id = %q, want %q", got.ProposalID, env.ProposalID)
		}
		if !strings.Contains(strings.Join(got.Files, ","), "listed.txt") {
			t.Errorf("list should name the changed file, got %v", got.Files)
		}
	})
}

// TestProposalRegistry_SessionIsolation pins yaah-yox's ownership rule: the
// registry belongs to the session that sealed the proposal. Under one process
// serving several sessions, another session's tool must not list it, show it
// (which prints its diff), or settle it.
func TestProposalRegistry_SessionIsolation(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	rt := RetainedRuntime{
		Runner:       writingRunner(t, "secret.txt").run(),
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}

	res, err := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "worker"}, 0, false)
	if err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	env := decodeProposalEnvelope(t, res)
	if env.Status != "proposal" || env.ProposalID == "" {
		t.Fatalf("want a sealed proposal, got %+v", env)
	}

	id := jsonString(env.ProposalID)
	owner := &ProposalTool{Proposals: rt.Proposals}
	other := &ProposalTool{Proposals: NewProposalRegistry()} // a second session

	t.Run("another session's list is empty", func(t *testing.T) {
		out, lerr := other.Execute(context.Background(), `{"action":"list"}`)
		if lerr != nil {
			t.Fatalf("list: %v", lerr)
		}
		var fields map[string]json.RawMessage
		if uerr := json.Unmarshal([]byte(out), &fields); uerr != nil {
			t.Fatalf("list result is not JSON: %v\n%s", uerr, out)
		}
		if got := strings.TrimSpace(string(fields["proposals"])); got != "[]" {
			t.Errorf("another session must not list this proposal: %s", out)
		}
	})

	t.Run("another session cannot show it", func(t *testing.T) {
		if out, serr := other.Execute(context.Background(), `{"action":"show","proposal_id":`+id+`}`); serr == nil {
			t.Errorf("show must not read another session's proposal (its diff leaked): %s", out)
		}
	})

	for _, action := range []string{"select", "apply", "release", "discard"} {
		t.Run("another session cannot "+action, func(t *testing.T) {
			if out, serr := other.Execute(context.Background(), `{"action":"`+action+`","proposal_id":`+id+`}`); serr == nil {
				t.Errorf("%s must be refused across sessions: %s", action, out)
			}
		})
	}

	t.Run("the owning session still settles it", func(t *testing.T) {
		if _, serr := owner.Execute(context.Background(), `{"action":"select","proposal_id":`+id+`}`); serr != nil {
			t.Fatalf("owner select: %v", serr)
		}
		if _, serr := owner.Execute(context.Background(), `{"action":"list","proposal_id":`+id+`}`); serr != nil {
			t.Fatalf("owner list: %v", serr)
		}
	})
}

// TestTeardownReportScratch_Notes pins yaah-atn's reporting rules for the report
// lane: a scratch that did not come down is described in a note the caller
// receives, and a clean teardown is silent.
func TestTeardownReportScratch_Notes(t *testing.T) {
	repo := t.TempDir() // not a git repo: nothing is registered
	gone := filepath.Join(t.TempDir(), "never-created")
	stranded := t.TempDir() // the scratch survived

	tests := []struct {
		name    string
		wt      string
		discard func() (bool, error)
		silent  bool
		want    []string
	}{
		{
			name:    "scope discarded and the worktree is gone",
			wt:      gone,
			discard: func() (bool, error) { return true, nil },
			silent:  true,
		},
		{
			name:    "discard failed and the worktree survived",
			wt:      stranded,
			discard: func() (bool, error) { return true, errors.New("boom: cannot discard") },
			want:    []string{"teardown failed", "boom: cannot discard", stranded},
		},
		{
			name:    "discard reported a failure but the worktree is gone",
			wt:      gone,
			discard: func() (bool, error) { return true, errors.New("boom") },
			want:    []string{"boom", "is gone"},
		},
		{
			name:    "scope missing and the worktree unregistered but still on disk",
			wt:      stranded,
			discard: func() (bool, error) { return false, nil },
			want:    []string{"left the worktree at " + stranded},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			note := teardownReportScratch(context.Background(), RetainedRuntime{RepoPath: repo}, tc.wt, tc.discard)
			if tc.silent {
				if note != "" {
					t.Errorf("a clean teardown must be silent, got %q", note)
				}
				return
			}
			for _, want := range tc.want {
				if !strings.Contains(note, want) {
					t.Errorf("note %q should mention %q", note, want)
				}
			}
		})
	}
}

// TestRetained_ReportLaneTeardownFailureSurfaced pins yaah-atn: the report lane
// registers no proposal, so a scratch that will not come down has no record to
// be recovered through. The result must say so — a log line is invisible to the
// orchestrator. A locked git worktree cannot be removed, which strands it
// exactly as a real failure does.
func TestRetained_ReportLaneTeardownFailureSurfaced(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	lockResults := make(chan error, 1)
	runner := &sessionRunner{}
	runner.sideEffect = func(call int, _ string) {
		wd, ok := runner.workdirAt(call)
		if !ok {
			lockResults <- fmt.Errorf("no workdir recorded for call %d", call)
			return
		}
		cmd := exec.Command("git", "worktree", "lock", wd)
		cmd.Dir = wd
		if out, err := cmd.CombinedOutput(); err != nil {
			lockResults <- fmt.Errorf("git worktree lock: %v: %s", err, out)
			return
		}
		lockResults <- nil
	}
	rt := newRetainedTestEnv(t, runner)

	res, err := startRetainedRun(context.Background(), rt, "review the thing", SubAgentParams{Role: "reviewer"}, 0, true)
	if err != nil {
		t.Fatalf("startRetainedRun(report): %v", err)
	}
	if lerr := <-lockResults; lerr != nil {
		t.Fatalf("%v", lerr)
	}
	if !strings.HasPrefix(res, "default") {
		t.Errorf("the report itself must survive the note, got %q", res)
	}

	wd := runner.workdirFor(t, 0)
	if _, statErr := os.Stat(wd); statErr != nil {
		t.Skipf("the worktree came down despite the lock (stat err=%v) — nothing was stranded", statErr)
	}
	if !strings.Contains(res, "note:") {
		t.Fatalf("a stranded worktree must be reported in the result, got %q", res)
	}
	if !strings.Contains(res, wd) {
		t.Errorf("the note must name the stranded worktree %s, got %q", wd, res)
	}
}

// TestRetainedWorktreePath_DefaultProjectLocal pins the default worktree root:
// when WorktreeRoot is unset, retained worktrees live at .yaah/shepherd-worktrees
// inside the repository working tree — not beside it.
func TestRetainedWorktreePath_DefaultProjectLocal(t *testing.T) {
	repo := t.TempDir()
	rt := RetainedRuntime{RepoPath: repo} // WorktreeRoot empty → default

	got := retainedWorktreePath(rt, "proposal:1/2")
	want := filepath.Join(repo, ".yaah", "shepherd-worktrees", "proposal-1-2")
	if got != want {
		t.Errorf("default path = %q, want %q", got, want)
	}
}

// TestRetainedWorktreePath_ExplicitRoot: a configured root is used verbatim and
// the proposal id is still sanitized into a safe directory name.
func TestRetainedWorktreePath_ExplicitRoot(t *testing.T) {
	root := t.TempDir()
	rt := RetainedRuntime{RepoPath: t.TempDir(), WorktreeRoot: root}

	got := retainedWorktreePath(rt, "proposal/1")
	want := filepath.Join(root, "proposal-1")
	if got != want {
		t.Errorf("explicit path = %q, want %q", got, want)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestEnsureWorktreeRootExcluded(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "t@example.com")
	runGit(t, repo, "config", "user.name", "test")

	rt := RetainedRuntime{RepoPath: repo} // default root: repo/.yaah/shepherd-worktrees

	if err := ensureWorktreeRootExcluded(context.Background(), rt); err != nil {
		t.Fatalf("ensureWorktreeRootExcluded: %v", err)
	}

	excludePath := filepath.Join(repo, ".git", "info", "exclude")
	data, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("read info/exclude: %v", err)
	}
	if !strings.Contains(string(data), ".yaah/shepherd-worktrees/") {
		t.Errorf("info/exclude does not contain the worktree root:\n%s", data)
	}

	// Idempotent: a second call must not duplicate the line.
	if err := ensureWorktreeRootExcluded(context.Background(), rt); err != nil {
		t.Fatalf("second ensureWorktreeRootExcluded: %v", err)
	}
	data, _ = os.ReadFile(excludePath)
	if got := strings.Count(string(data), ".yaah/shepherd-worktrees/"); got != 1 {
		t.Errorf("info/exclude contains the line %d times, want 1", got)
	}
}

func TestEnsureWorktreeRootExcluded_OutsideRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := t.TempDir()
	runGit(t, repo, "init", "-q")

	outside := t.TempDir()
	rt := RetainedRuntime{RepoPath: repo, WorktreeRoot: outside}

	if err := ensureWorktreeRootExcluded(context.Background(), rt); err != nil {
		t.Fatalf("ensureWorktreeRootExcluded: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("read info/exclude: %v", err)
	}
	if strings.Contains(string(data), "shepherd-worktrees") {
		t.Errorf("info/exclude unexpectedly modified for an outside root:\n%s", data)
	}
}

// TestRetained_ReportLaneReturnsPlainResult pins yaah-7yf's report lane:
// the dispatch keeps its isolated worktree, but the result is the plain
// report text, no proposal is registered, and the worktree is gone when
// the call returns — nothing to settle.
func TestRetained_ReportLaneReturnsPlainResultAndDiscards(t *testing.T) {
	runner := writingRunner(t, "out.txt")
	rt := newRetainedTestEnv(t, runner)

	res, err := startRetainedRun(context.Background(), rt, "review the thing", SubAgentParams{Role: "reviewer"}, 50*time.Millisecond, true)
	if err != nil {
		t.Fatalf("startRetainedRun(report): %v", err)
	}
	if res != "default" {
		t.Errorf("report lane must return the runner's text verbatim, got %q", res)
	}
	if strings.HasPrefix(strings.TrimSpace(res), "{") {
		t.Errorf("report lane must not return a proposal envelope, got %q", res)
	}

	// No proposal was registered: nothing to settle.
	if got := rt.Proposals.summaries(); got != "No open proposals." {
		t.Errorf("report lane must not register a proposal, got %q", got)
	}

	// The worktree existed during the run (the write landed) and is gone now.
	wd := runner.workdirFor(t, 0)
	if wd == "" || wd == rt.RepoPath {
		t.Fatalf("the dispatch must run in an isolated worktree, got %q", wd)
	}
	if _, err := os.Stat(wd); !os.IsNotExist(err) {
		t.Errorf("report worktree must be torn down (stat err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(rt.RepoPath, "out.txt")); !os.IsNotExist(err) {
		t.Errorf("the host tree must be untouched (stat err=%v)", err)
	}
}

// TestRetained_ReportLaneBodyErrorPropagates: a failed report run discards
// the worktree and surfaces the error — the delegate layer classifies it
// the same way it classifies shared-lane failures.
func TestRetained_ReportLaneBodyErrorPropagates(t *testing.T) {
	runner := &sessionRunner{}
	runner.responses = []runnerResponse{{waitForContext: true}}
	rt := newRetainedTestEnv(t, runner)

	res, err := startRetainedRun(context.Background(), rt, "review", SubAgentParams{Role: "reviewer"}, 50*time.Millisecond, true)
	if err == nil {
		t.Fatalf("report lane body error must propagate, got result %q", res)
	}
	if got := rt.Proposals.summaries(); got != "No open proposals." {
		t.Errorf("failed report run must not register a proposal, got %q", got)
	}
	wd := runner.workdirFor(t, 0)
	if wd != "" {
		if _, statErr := os.Stat(wd); !os.IsNotExist(statErr) {
			t.Errorf("failed report run must tear down the worktree (stat err=%v)", statErr)
		}
	}
}

func findRetainedFacts(t *testing.T, store *shepherdtrace.SQLiteTraceStore, owner, schema string) []shepherdtrace.Record {
	t.Helper()
	slice, err := store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, owner, 999, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read trace for %s: %v", owner, err)
	}
	var out []shepherdtrace.Record
	for _, id := range slice.FactIDs() {
		if rec, ok := slice.FactsByID[id].(shepherdtrace.Record); ok && rec.Envelope.SchemaRef == schema {
			out = append(out, rec)
		}
	}
	return out
}

func TestRetained_ReviewProvenance(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	rt := RetainedRuntime{
		Runner:       writingRunner(t, "out.txt").run(),
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}

	res, err := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "worker"}, 0, false)
	if err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	env := decodeProposalEnvelope(t, res)
	if env.Status != "proposal" {
		t.Fatalf("proposal envelope = %+v", env)
	}
	proposals := findRetainedFacts(t, fx.store, env.ProposalID, goatherd.SchemaMergeProposed)
	if len(proposals) != 1 {
		t.Fatalf("proposal facts = %d, want 1", len(proposals))
	}

	root, ok := fx.mgr.Get(rootID)
	if !ok {
		t.Fatal("root scope missing")
	}
	if _, err := rt.Proposals.settleRetainedProposal(context.Background(), env.ProposalID, "select"); err != nil {
		t.Fatalf("select: %v", err)
	}
	decisions := findRetainedFacts(t, fx.store, root.OwnerID(), goatherd.SchemaSupervisorDecision)
	if len(decisions) != 1 {
		t.Fatalf("decision facts = %d, want 1", len(decisions))
	}
	if fmt.Sprint(decisions[0].Body.Payload["approved"]) != "true" {
		t.Errorf("decision approved = %v, want true", decisions[0].Body.Payload["approved"])
	}
	if fmt.Sprint(decisions[0].Body.Payload["action"]) != string(goatherd.SettleSelect) {
		t.Errorf("decision action = %v, want %s", decisions[0].Body.Payload["action"], goatherd.SettleSelect)
	}
}

func TestRetained_ReviewProvenanceDiscard(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	rt := RetainedRuntime{
		Runner:       writingRunner(t, "out.txt").run(),
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}
	res, err := startRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "worker"}, 0, false)
	if err != nil {
		t.Fatalf("startRetainedRun: %v", err)
	}
	env := decodeProposalEnvelope(t, res)
	root, _ := fx.mgr.Get(rootID)
	if _, err := rt.Proposals.settleRetainedProposal(context.Background(), env.ProposalID, "discard"); err != nil {
		t.Fatalf("discard: %v", err)
	}
	decisions := findRetainedFacts(t, fx.store, root.OwnerID(), goatherd.SchemaSupervisorDecision)
	if len(decisions) != 1 {
		t.Fatalf("decision facts = %d, want 1", len(decisions))
	}
	if fmt.Sprint(decisions[0].Body.Payload["approved"]) != "false" {
		t.Errorf("decision approved = %v, want false", decisions[0].Body.Payload["approved"])
	}
	if fmt.Sprint(decisions[0].Body.Payload["action"]) != string(goatherd.SettleDiscard) {
		t.Errorf("decision action = %v, want %s", decisions[0].Body.Payload["action"], goatherd.SettleDiscard)
	}
}
