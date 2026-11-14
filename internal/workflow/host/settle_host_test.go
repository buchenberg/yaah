package host

// settle_host_test.go pins the proposal lane and the settle door
// (yaah-0u0v.13): a proposal-lane delegate seals a usable handle whose id
// reaches the event log, settle nodes dispose through dispatch.Settle with
// the door owning the path_disjoint default, and every one of the four verbs
// round-trips through the real retained machinery — a real temp git repo, a
// real worktree, a scripted runner standing in for the model.

import (
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherd "github.com/buchenberg/goatherd-runtime"
	goatherdgit "github.com/buchenberg/goatherd-runtime/sandbox/git"

	"github.com/buchenberg/yaah/internal/dispatch"
	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/workflow/exec"
)

// retainedEnv builds the wiring the composition root injects for a live
// session: an in-memory trace store with its scope manager, a real temp git
// repo, a root scope over it, and a fresh proposal registry.
func retainedEnv(t *testing.T, runner jobs.TaskRunner) *dispatch.RetainedRuntime {
	t.Helper()
	if _, err := osexec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	store, err := shepherdtrace.NewSQLiteTraceStore(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	mgr := goatherd.NewScopeManager(store)

	repo := t.TempDir()
	// Isolate from the developer's global/system git config.
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "gitconfig-system"))
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		mustGit(t, repo, args...)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "-m", "init")

	root, err := mgr.Create("wf-host-test-root", goatherdgit.NewLocalGitSandbox(repo,
		goatherdgit.WithCleanPolicy(goatherdgit.CleanPreserveUntracked)))
	if err != nil {
		t.Fatalf("create root scope: %v", err)
	}
	return &dispatch.RetainedRuntime{
		Runner:       runner,
		RepoPath:     repo,
		ScopeManager: mgr,
		RootScopeID:  root.ID(),
		WorktreeRoot: t.TempDir(),
		Proposals:    dispatch.NewProposalRegistry(),
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := osexec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// writeFileRunner stands in for a sub-agent: it writes one file into the
// worktree it is handed, which is the side effect a proposal seals.
func writeFileRunner(name, content string) jobs.TaskRunner {
	return func(_ context.Context, _ string, params jobs.SubAgentParams) (string, error) {
		if err := os.WriteFile(filepath.Join(params.Workdir, name), []byte(content), 0o644); err != nil {
			return "", err
		}
		return "wrote " + name, nil
	}
}

const settleChainSpec = `  steps:
    - id: work
      kind: delegate
      role: developer
      lane: proposal
      prompt: do the work
    - id: land
      kind: settle
      needs: [work]
      proposals: { from: steps.work.output.proposal_id }
      action: select
`

// TestProposalDelegateSealsAndSettleSelects is the acceptance path end to
// end: the proposal-lane delegate returns a usable handle, the event log
// records its id, the settle node selects it, and the sealed file lands on
// the host repo.
func TestProposalDelegateSealsAndSettleSelects(t *testing.T) {
	y := strings.Replace(specHead, "%s", "settle-e2e", 1) + settleChainSpec
	env := retainedEnv(t, writeFileRunner("change.txt", "from the sub-agent\n"))

	res, err := Execute(context.Background(), Options{
		Graph:    graphFromYAML(t, y),
		Params:   map[string]any{"task": "settle e2e"},
		Runner:   env.Runner,
		Retained: env,
	})
	if err != nil {
		t.Fatalf("Execute: %v\nlog:\n%s", err, logOf(res.Run))
	}
	if res.Run.Status != exec.RunSucceeded {
		t.Fatalf("status = %s reason=%q, want succeeded\nlog:\n%s",
			res.Run.Status, res.Run.Reason, logOf(res.Run))
	}

	log := logOf(res.Run)
	work := nodeState(t, res.Run, "steps.work")
	if work.Proposal == nil || work.Proposal.ProposalID == "" {
		t.Fatalf("work node recorded no proposal handle\nlog:\n%s", log)
	}
	if !strings.Contains(log, "proposal_id="+work.Proposal.ProposalID) {
		t.Errorf("event log never records the proposal id\nlog:\n%s", log)
	}
	land := nodeState(t, res.Run, "steps.land")
	if len(land.Settled) != 1 || land.Settled[0].Status != "settled" || land.Settled[0].Action != "select" {
		t.Fatalf("land settled = %+v, want one select settlement\nlog:\n%s", land.Settled, log)
	}
	if _, err := os.Stat(filepath.Join(env.RepoPath, "change.txt")); err != nil {
		t.Errorf("select must land the sealed file on the host repo: %v", err)
	}
}

// TestSettleDoorVerbs exercises apply, release, and discard through the door
// against the real retained machinery (select is covered end to end above):
// one sealed proposal per verb, one Settle call, and the host repo gains the
// file only when the verb lands work.
func TestSettleDoorVerbs(t *testing.T) {
	for _, tc := range []struct {
		verb    string
		landed  bool
		wantErr bool
	}{
		{verb: "apply", landed: true},
		{verb: "release", landed: false},
		{verb: "discard", landed: false},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			name := "verb-" + tc.verb + ".txt"
			env := retainedEnv(t, writeFileRunner(name, "sealed work\n"))
			y := strings.Replace(specHead, "%s", "verb-"+tc.verb, 1) + settleChainSpec
			g := graphFromYAML(t, y)

			rt := NewRuntime(env.Runner)
			rt.Retained = env
			h, err := rt.Start(context.Background(), exec.Dispatch{
				Node:       g.Steps[0],
				Attempt:    1,
				DispatchID: "steps.work#1",
			})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			c := awaitCompletion(t, rt, h)
			if c.ProposalID == "" {
				t.Fatalf("completion sealed no proposal: %+v", c)
			}

			s, err := rt.Settle(context.Background(), exec.SettleRequest{
				NodePath:   "steps.land",
				ProposalID: c.ProposalID,
				Producer:   "steps.work",
				Verb:       tc.verb,
			})
			if err != nil {
				t.Fatalf("Settle(%s): %v", tc.verb, err)
			}
			if s.Status != "settled" || s.Action != tc.verb || s.LeftOpen {
				t.Errorf("settled = %+v, want status=settled action=%s, nothing open", s, tc.verb)
			}
			_, statErr := os.Stat(filepath.Join(env.RepoPath, name))
			if tc.landed && statErr != nil {
				t.Errorf("%s must land the work on the host repo: %v", tc.verb, statErr)
			}
			if !tc.landed && statErr == nil {
				t.Errorf("%s must not land the work on the host repo", tc.verb)
			}
		})
	}
}

// TestSettleDoorOwnsTheDefaultPolicy pins that an empty policy reaches
// dispatch.Settle as path_disjoint — the default is the door's constant.
func TestSettleDoorOwnsTheDefaultPolicy(t *testing.T) {
	if DefaultSettleStrategy != "path_disjoint" {
		t.Errorf("DefaultSettleStrategy = %q, want path_disjoint", DefaultSettleStrategy)
	}
}

// TestSettleWithoutRetainedRuntimeErrors pins the honest refusal: no
// registry, no settlement — and the proposal is reported left open, because
// nothing disposed of it.
func TestSettleWithoutRetainedRuntimeErrors(t *testing.T) {
	rt := NewRuntime(func(context.Context, string, jobs.SubAgentParams) (string, error) {
		return "unused", nil
	})
	s, err := rt.Settle(context.Background(), exec.SettleRequest{
		NodePath: "steps.land", ProposalID: "prop-1", Verb: "select",
	})
	if err == nil {
		t.Fatal("Settle without a retained runtime must fail")
	}
	if s.Status != "error" || !s.LeftOpen {
		t.Errorf("settled = %+v, want an error status with the proposal left open", s)
	}
}

// TestSettleRefusesNonVerb pins the door's own vocabulary guard: continue
// and fork are delegate re-run bindings, and the door refuses them even if a
// caller smuggles one past the scheduler.
func TestSettleRefusesNonVerb(t *testing.T) {
	env := retainedEnv(t, writeFileRunner("unused.txt", "x\n"))
	rt := NewRuntime(env.Runner)
	rt.Retained = env
	s, err := rt.Settle(context.Background(), exec.SettleRequest{
		NodePath: "steps.land", ProposalID: "prop-1", Verb: "continue",
	})
	if err == nil {
		t.Fatal("Settle(continue) must fail")
	}
	if s.Status != "error" || !s.LeftOpen || !strings.Contains(s.Error, "select|apply|release|discard") {
		t.Errorf("settled = %+v, want a vocabulary refusal leaving the proposal open", s)
	}
}

func awaitCompletion(t *testing.T, rt *Runtime, h exec.Handle) exec.Completion {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if c, ok := rt.Completed(h); ok {
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no completion for %s within 60s", h.DispatchID)
	return exec.Completion{}
}
