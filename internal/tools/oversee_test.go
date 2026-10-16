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

// overseeRunner scripts the two dispatch kinds the loop makes, by call order:
// even calls are workers (they write a file into their worktree and return a
// report), odd calls are reviewers (they return the next scripted verdict). The
// order is deterministic — worker, reviewer, worker, reviewer — which is exactly
// what the real loop does.
type overseeRunner struct {
	t *testing.T

	mu       sync.Mutex
	prompts  []string
	workdirs []string
	roles    []string

	results  []string // per worker call
	verdicts []string // per review call

	// onReview runs inside a review call, before its verdict is returned and
	// before any settle tears the worktree down, so a test can inspect the world
	// mid-loop.
	onReview func(round int, reviewerWorkdir string)
}

func (r *overseeRunner) run() TaskRunner {
	return func(_ context.Context, prompt string, params SubAgentParams) (string, error) {
		r.mu.Lock()
		call := len(r.prompts)
		r.prompts = append(r.prompts, prompt)
		r.workdirs = append(r.workdirs, params.Workdir)
		r.roles = append(r.roles, params.Role)
		r.mu.Unlock()

		if call%2 == 0 {
			n := call / 2
			writeTestFile(r.t, params.Workdir, fmt.Sprintf("out%d.txt", n), fmt.Sprintf("attempt %d\n", n))
			r.mu.Lock()
			defer r.mu.Unlock()
			if n < len(r.results) {
				return r.results[n], nil
			}
			return fmt.Sprintf("did attempt %d", n), nil
		}

		n := call / 2
		if r.onReview != nil {
			r.onReview(n+1, params.Workdir)
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if n < len(r.verdicts) {
			return r.verdicts[n], nil
		}
		return "VERDICT: accept", nil
	}
}

func (r *overseeRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.prompts)
}

func (r *overseeRunner) roleAt(i int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.roles[i]
}

func (r *overseeRunner) workdirAt(i int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.workdirs[i]
}

func (r *overseeRunner) promptAt(i int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prompts[i]
}

// openOverseeProposals counts proposals still awaiting settlement.
func openOverseeProposals() int {
	retainedProposals.mu.Lock()
	defer retainedProposals.mu.Unlock()
	return len(retainedProposals.byID)
}

func decodeOverseeEnvelope(t *testing.T, result string) overseeEnvelope {
	t.Helper()
	var env overseeEnvelope
	if err := json.Unmarshal([]byte(result), &env); err != nil {
		t.Fatalf("oversee envelope is not JSON: %v\n%s", err, result)
	}
	return env
}

// newOverseeTool wires the tool over a real temp repo / scope manager and the
// scripted runner.
func newOverseeTool(t *testing.T, runner *overseeRunner) (*OverseeTool, string) {
	t.Helper()
	fx, repo, rootID := newSessionFixtures(t)
	_ = fx
	rt := RetainedRuntime{
		Runner:       runner.run(),
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
	}
	return &OverseeTool{Runtime: &rt}, repo
}

func runOversee(t *testing.T, tool *OverseeTool, args string) overseeEnvelope {
	t.Helper()
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("oversee: %v", err)
	}
	return decodeOverseeEnvelope(t, res)
}

// TestOversee_AcceptsFirstRound: an accepted first round settles once, and the
// reviewer inspects the worker's own world.
func TestOversee_AcceptsFirstRound(t *testing.T) {
	resetRetainedProposals()
	runner := &overseeRunner{t: t, verdicts: []string{"Looks right.\n\nVERDICT: accept"}}
	tool, repo := newOverseeTool(t, runner)
	// The reviewer's world IS the worker's world, with the change applied — and
	// the assertion has to happen in-loop, because an accepted settle tears the
	// worktree down.
	//
	// The review dispatch runs in that worktree, so a confined reviewer plants
	// its scratch there AFTER the proposal was sealed. That must not reach the
	// accepted tree: the settle applies the sealed delta, not the directory.
	sawReviewerScratch := false
	runner.onReview = func(_ int, wd string) {
		if _, err := os.Stat(filepath.Join(wd, "out0.txt")); err != nil {
			t.Errorf("the reviewer must see the worker's change in its world: %v", err)
		}
		if _, err := os.Stat(filepath.Join(wd, confinementScratchName)); err == nil {
			sawReviewerScratch = true
		}
	}

	env := runOversee(t, tool, `{"task":"write out0.txt"}`)
	if env.Status != "accepted" || env.Action != "select" || env.Rounds != 1 {
		t.Fatalf("envelope = %+v, want accepted/select round 1", env)
	}
	if runner.callCount() != 2 {
		t.Fatalf("want 1 worker + 1 review, got %d dispatches", runner.callCount())
	}
	if got := runner.roleAt(0); got != "developer" {
		t.Errorf("worker role = %q, want the default developer", got)
	}
	if got := runner.roleAt(1); got != "reviewer" {
		t.Errorf("reviewer role = %q, want the default reviewer", got)
	}
	// It inspected the post-change tree, not a diff transcript.
	if runner.workdirAt(0) != runner.workdirAt(1) {
		t.Errorf("reviewer workdir %q != worker workdir %q", runner.workdirAt(1), runner.workdirAt(0))
	}
	if _, err := os.Stat(filepath.Join(repo, "out0.txt")); err != nil {
		t.Errorf("acceptance must land the work on the host: %v", err)
	}
	if sawReviewerScratch {
		if _, err := os.Stat(filepath.Join(repo, confinementScratchName)); !os.IsNotExist(err) {
			t.Errorf("the reviewer's sandbox scratch must not land in the accepted tree (stat err=%v)", err)
		}
	}
	if n := openOverseeProposals(); n != 0 {
		t.Errorf("an accepted proposal is settled, not left open (got %d)", n)
	}
}

// TestOversee_RevisesThenAccepts: a rejected round's work NEVER lands, its
// proposal is discarded, and the reason reaches the next worker.
func TestOversee_RevisesThenAccepts(t *testing.T) {
	resetRetainedProposals()
	runner := &overseeRunner{
		t: t,
		verdicts: []string{
			"VERDICT: revise\nREASON: out0.txt is empty — write the content",
			"VERDICT: accept",
		},
	}
	tool, repo := newOverseeTool(t, runner)

	// Mid-loop, after round 1 was rejected, the host must still be untouched.
	runner.onReview = func(round int, _ string) {
		if round != 1 {
			return
		}
		if _, err := os.Stat(filepath.Join(repo, "out0.txt")); !os.IsNotExist(err) {
			t.Errorf("round 1's rejected work must never land (stat err=%v)", err)
		}
	}

	env := runOversee(t, tool, `{"task":"write it","max_rounds":3}`)
	if env.Status != "accepted" || env.Rounds != 2 {
		t.Fatalf("envelope = %+v, want accepted on round 2", env)
	}
	// Round 2's work landed; round 1's did not.
	if _, err := os.Stat(filepath.Join(repo, "out1.txt")); err != nil {
		t.Errorf("the accepted round's work must land: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "out0.txt")); !os.IsNotExist(err) {
		t.Errorf("the discarded round's work must not land (stat err=%v)", err)
	}
	// The feedback reached the second worker's prompt.
	if p := runner.promptAt(2); !strings.Contains(p, "out0.txt is empty") || !strings.Contains(p, "REVIEW GUIDANCE") {
		t.Errorf("round 2's worker prompt must carry the reviewer's reason:\n%s", p)
	}
	// Every worktree is gone: one settled, one discarded.
	if n := openOverseeProposals(); n != 0 {
		t.Errorf("no proposal may be left open (got %d)", n)
	}
}

// TestOversee_Exhausts: when no round is accepted, nothing lands and nothing is
// left open.
func TestOversee_Exhausts(t *testing.T) {
	resetRetainedProposals()
	runner := &overseeRunner{
		t:        t,
		verdicts: []string{"VERDICT: revise\nREASON: still wrong", "VERDICT: revise\nREASON: still wrong"},
	}
	tool, repo := newOverseeTool(t, runner)

	env := runOversee(t, tool, `{"task":"write it","max_rounds":2}`)
	if env.Status != "exhausted" || env.Rounds != 2 {
		t.Fatalf("envelope = %+v, want exhausted after 2 rounds", env)
	}
	if runner.callCount() != 4 {
		t.Fatalf("want 2 workers + 2 reviews, got %d dispatches", runner.callCount())
	}
	entries, _ := os.ReadDir(repo)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "out") {
			t.Errorf("nothing may land when every round is rejected, found %s", e.Name())
		}
	}
	if n := openOverseeProposals(); n != 0 {
		t.Errorf("every rejected proposal must be discarded (got %d open)", n)
	}
	if !strings.Contains(env.Review, "still wrong") {
		t.Errorf("the report must carry the last verdict, got %q", env.Review)
	}
	if env.Result == "" {
		t.Error("the report must carry the worker's own result")
	}
}

// TestOversee_UnparseableVerdictIsNotAcceptance: prose is not a verdict, and
// nothing may land on prose alone.
func TestOversee_UnparseableVerdictIsNotAcceptance(t *testing.T) {
	resetRetainedProposals()
	runner := &overseeRunner{
		t:        t,
		verdicts: []string{"This looks good to me, ship it!", "VERDICT: accept"},
	}
	tool, _ := newOverseeTool(t, runner)

	env := runOversee(t, tool, `{"task":"write it","max_rounds":3}`)
	if env.Status != "accepted" || env.Rounds != 2 {
		t.Fatalf("a verdict-less review must not accept: envelope = %+v", env)
	}
	if !strings.Contains(env.Review, "no verdict") && !strings.Contains(env.Review, "accept") {
		t.Errorf("the report should show what was treated as the round-2 verdict, got %q", env.Review)
	}
}

// TestOversee_SettleNoneHandsBack: the orchestrator can keep the decision.
func TestOversee_SettleNoneHandsBack(t *testing.T) {
	resetRetainedProposals()
	runner := &overseeRunner{t: t, verdicts: []string{"VERDICT: accept"}}
	tool, repo := newOverseeTool(t, runner)

	env := runOversee(t, tool, `{"task":"write it","settle":"none"}`)
	if env.Status != "proposed" || env.Action != "none" {
		t.Fatalf("envelope = %+v, want proposed/none", env)
	}
	if env.ProposalID == "" {
		t.Fatal("a handed-back proposal must carry its id")
	}
	if _, err := os.Stat(filepath.Join(repo, "out0.txt")); !os.IsNotExist(err) {
		t.Errorf("settle:none must not land anything (stat err=%v)", err)
	}
	if n := openOverseeProposals(); n != 1 {
		t.Errorf("the accepted proposal must stay open for the orchestrator (got %d)", n)
	}
	// And it is really settleable by the orchestrator's own tool.
	if _, err := settleRetainedProposal(context.Background(), env.ProposalID, "select"); err != nil {
		t.Fatalf("the handed-back proposal must settle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "out0.txt")); err != nil {
		t.Errorf("the orchestrator's settle must land it: %v", err)
	}
}

// TestOversee_Validation: the loop refuses what it cannot do, rather than
// guessing.
func TestOversee_Validation(t *testing.T) {
	resetRetainedProposals()
	runner := &overseeRunner{t: t}
	tool, _ := newOverseeTool(t, runner)

	for _, tc := range []struct{ name, args string }{
		{"missing task", `{}`},
		{"blank task", `{"task":"   "}`},
		{"rounds above the cap", `{"task":"x","max_rounds":11}`},
		{"unknown settle", `{"task":"x","settle":"merge"}`},
	} {
		if _, err := tool.Execute(context.Background(), tc.args); err == nil {
			t.Errorf("%s: want an error, got none", tc.name)
		}
	}
	if runner.callCount() != 0 {
		t.Errorf("a refused call must dispatch nothing, got %d", runner.callCount())
	}

	// No retained lane, no oversee.
	bare := &OverseeTool{}
	if _, err := bare.Execute(context.Background(), `{"task":"x"}`); err == nil {
		t.Error("oversee must refuse without the retained lane")
	}

	// max_rounds 0 means the default, not zero rounds.
	runner2 := &overseeRunner{t: t, verdicts: []string{"VERDICT: revise\nREASON: no", "VERDICT: revise\nREASON: no", "VERDICT: revise\nREASON: no"}}
	tool2, _ := newOverseeTool(t, runner2)
	env := runOversee(t, tool2, `{"task":"x","max_rounds":0}`)
	if env.Status != "exhausted" || env.Rounds != DefaultOverseeRounds {
		t.Fatalf("max_rounds 0 must use the default (%d), got %+v", DefaultOverseeRounds, env)
	}
}

// TestParseOverseeVerdict pins the contract the loop trusts: the LAST verdict
// wins, a trailing contract block is not feedback, and no verdict is not
// acceptance.
func TestParseOverseeVerdict(t *testing.T) {
	cases := []struct {
		name        string
		review      string
		wantVerdict string
		wantReason  string
	}{
		{"accept", "VERDICT: accept", "accept", ""},
		{"revise", "VERDICT: revise\nREASON: fix the test", "revise", "fix the test"},
		{"last wins", "I first thought accept.\nVERDICT: accept\nOn reflection:\nVERDICT: revise\nREASON: no", "revise", "no"},
		{"emphasis tolerated", "**VERDICT: Accept**", "accept", ""},
		{"no verdict", "looks fine by me", "", ""},
		{"stops at a contract block", "VERDICT: revise\nREASON: fix it\n## Metrics\n- files: 1", "revise", "fix it"},
	}
	for _, c := range cases {
		v, r := parseOverseeVerdict(c.review)
		if v != c.wantVerdict {
			t.Errorf("%s: verdict = %q, want %q", c.name, v, c.wantVerdict)
		}
		if r != c.wantReason {
			t.Errorf("%s: reason = %q, want %q", c.name, r, c.wantReason)
		}
	}
}
