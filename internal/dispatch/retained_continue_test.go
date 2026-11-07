package dispatch

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/types"
)

// TestRetained_ContinueSeedsFromSealedProposal is the core "continue for N more
// turns" path: a turn-exhausted worktree proposal is re-run in a fresh clone
// seeded from its sealed workspace and captured conversation, the result is
// sealed as a new proposal, and the original is consumed.
func TestRetained_ContinueSeedsFromSealedProposal(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)

	first := []types.Message{types.SystemMsg("sys"), types.UserMsg("do the work")}
	var calls int
	var firstWorkdir, secondWorkdir string
	var sawSeed []types.Message
	var sawTurns int

	rt := RetainedRuntime{
		Runner: func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
			calls++
			if calls == 1 {
				firstWorkdir = params.Workdir
				writeTestFile(t, params.Workdir, "one.txt", "first\n")
				jobs.CaptureSubAgentTranscript(ctx, first)
				return "first result", nil
			}
			secondWorkdir = params.Workdir
			sawSeed = params.SeedMessages
			sawTurns = params.MaxToolTurns
			writeTestFile(t, params.Workdir, "two.txt", "second\n")
			jobs.CaptureSubAgentTranscript(ctx, append(append([]types.Message{}, first...), types.UserMsg("continue")))
			return "second result", nil
		},
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}

	res, err := StartRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "developer"}, 10*time.Second, false)
	if err != nil {
		t.Fatalf("StartRetainedRun: %v", err)
	}
	orig := decodeProposalEnvelope(t, res)
	if orig.Status != "proposal" {
		t.Fatalf("original status = %q, want proposal (%s)", orig.Status, orig.Error)
	}

	res2, err := ContinueRetainedRun(context.Background(), rt, orig.ProposalID, 3, "", true)
	if err != nil {
		t.Fatalf("ContinueRetainedRun: %v", err)
	}
	cont := decodeProposalEnvelope(t, res2)
	if cont.Status != "proposal" {
		t.Fatalf("continuation status = %q, want proposal (%s)", cont.Status, cont.Error)
	}
	if cont.ProposalID == "" || cont.ProposalID == orig.ProposalID {
		t.Fatalf("continuation id = %q, want a new id distinct from %q", cont.ProposalID, orig.ProposalID)
	}
	if firstWorkdir == "" || secondWorkdir == "" {
		t.Fatalf("workdirs not recorded: first=%q second=%q", firstWorkdir, secondWorkdir)
	}
	if firstWorkdir == secondWorkdir {
		t.Fatalf("continuation reused worktree %q; want a fresh clone", firstWorkdir)
	}
	if !reflect.DeepEqual(sawSeed, first) {
		t.Fatalf("continuation seed = %+v, want the original conversation %+v", sawSeed, first)
	}
	if sawTurns != 3 {
		t.Errorf("continuation MaxToolTurns = %d, want 3", sawTurns)
	}
	for _, want := range []string{"one.txt", "two.txt"} {
		if !slices.Contains(cont.Files, want) {
			t.Errorf("continuation files %v missing %q (seeded work + new work)", cont.Files, want)
		}
	}

	// The original is consumed, so it is no longer settle-able.
	if _, err := rt.Proposals.get(orig.ProposalID); err == nil {
		t.Errorf("original proposal %s is still open after continue; it must be consumed", orig.ProposalID)
	}
}

// TestRetained_ForkLeavesOriginalOpen: fork re-runs the same way but keeps the
// original proposal so the orchestrator can compare both continuations.
func TestRetained_ForkLeavesOriginalOpen(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	seed := []types.Message{types.UserMsg("do the work")}

	rt := RetainedRuntime{
		Runner: func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
			writeTestFile(t, params.Workdir, "work.txt", "work\n")
			jobs.CaptureSubAgentTranscript(ctx, seed)
			return "done", nil
		},
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}

	res, err := StartRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "developer"}, 10*time.Second, false)
	if err != nil {
		t.Fatalf("StartRetainedRun: %v", err)
	}
	orig := decodeProposalEnvelope(t, res)

	res2, err := ContinueRetainedRun(context.Background(), rt, orig.ProposalID, 2, "", false)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	cont := decodeProposalEnvelope(t, res2)
	if cont.Status != "proposal" {
		t.Fatalf("fork status = %q, want proposal (%s)", cont.Status, cont.Error)
	}

	if _, err := rt.Proposals.get(orig.ProposalID); err != nil {
		t.Errorf("fork consumed the original: %v; want it to remain open", err)
	}
	if _, err := rt.Proposals.get(cont.ProposalID); err != nil {
		t.Errorf("forked proposal is not open: %v", err)
	}
}

// TestProposalTool_ContinueAction exercises the tool surface: the continue
// action parses extra_turns, routes through the retained runtime, and refuses
// when no runtime was wired.
func TestProposalTool_ContinueAction(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	seed := []types.Message{types.UserMsg("go")}
	rt := &RetainedRuntime{
		Runner: func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
			writeTestFile(t, params.Workdir, "work.txt", "work\n")
			jobs.CaptureSubAgentTranscript(ctx, seed)
			return "done", nil
		},
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}

	res, err := StartRetainedRun(context.Background(), *rt, "go", SubAgentParams{Role: "developer"}, 10*time.Second, false)
	if err != nil {
		t.Fatalf("StartRetainedRun: %v", err)
	}
	orig := decodeProposalEnvelope(t, res)

	tool := &ProposalTool{Proposals: rt.Proposals, Runtime: rt}
	out, err := tool.Execute(context.Background(), `{"action":"continue","proposal_id":`+jsonString(orig.ProposalID)+`,"extra_turns":2}`)
	if err != nil {
		t.Fatalf("continue action: %v", err)
	}
	cont := decodeProposalEnvelope(t, out)
	if cont.Status != "proposal" {
		t.Fatalf("continue status = %q, want proposal (%s)", cont.Status, cont.Error)
	}

	// Without the runtime the action refuses rather than guessing a runner.
	if _, err := (&ProposalTool{Proposals: rt.Proposals}).Execute(context.Background(), `{"action":"continue","proposal_id":"x","extra_turns":1}`); err == nil {
		t.Fatal("continue without a runtime must fail loudly")
	}
}

// TestRetained_ContinueRefusesWithoutConversation: a proposal whose dispatch
// captured no conversation (report/older run) cannot be continued — the guard
// fails loudly instead of silently starting cold, and the refusal leaves the
// original open.
func TestRetained_ContinueRefusesWithoutConversation(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	rt := RetainedRuntime{
		Runner: func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
			writeTestFile(t, params.Workdir, "work.txt", "work\n")
			// Deliberately capture no transcript.
			return "done", nil
		},
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}

	res, err := StartRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "developer"}, 10*time.Second, false)
	if err != nil {
		t.Fatalf("StartRetainedRun: %v", err)
	}
	orig := decodeProposalEnvelope(t, res)

	_, err = ContinueRetainedRun(context.Background(), rt, orig.ProposalID, 2, "", true)
	if err == nil || !strings.Contains(err.Error(), "no captured conversation") {
		t.Fatalf("err = %v, want the no-conversation guard", err)
	}
	if _, gerr := rt.Proposals.get(orig.ProposalID); gerr != nil {
		t.Errorf("refused continue consumed the original: %v", gerr)
	}
}

// TestRetained_StopReasonAndNotes: the envelope reports why the run ended, and
// the notes distinguish continue (consumes) from fork (preserves), flagging a
// byte-identical continuation.
func TestRetained_StopReasonAndNotes(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	seed := []types.Message{types.UserMsg("go")}
	rt := RetainedRuntime{
		Runner: func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
			writeTestFile(t, params.Workdir, "work.txt", "work\n")
			jobs.CaptureSubAgentTranscript(ctx, seed)
			jobs.CaptureSubAgentStop(ctx, jobs.SubAgentStop{TurnBudgetExhausted: true})
			return "done", nil
		},
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}

	res, err := StartRetainedRun(context.Background(), rt, "go", SubAgentParams{Role: "developer"}, 10*time.Second, false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	env := decodeProposalEnvelope(t, res)
	if env.StopReason != "turn_budget" {
		t.Errorf("dispatch stop_reason = %q, want turn_budget", env.StopReason)
	}

	// Fork: preserves the parent, notes read "fork of".
	resFork, err := ContinueRetainedRun(context.Background(), rt, env.ProposalID, 2, "", false)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	fork := decodeProposalEnvelope(t, resFork)
	if fork.StopReason != "turn_budget" {
		t.Errorf("fork stop_reason = %q, want turn_budget", fork.StopReason)
	}
	if !strings.HasPrefix(fork.Notes, "fork of ") || strings.Contains(fork.Notes, "original discarded") {
		t.Errorf("fork notes = %q, want a preserving 'fork of' note", fork.Notes)
	}
	// The runner writes the same file the source already had, so the
	// continuation is byte-identical and must say so.
	if !strings.Contains(fork.Notes, "no delta vs parent") {
		t.Errorf("fork notes = %q, want a no-delta note", fork.Notes)
	}

	// Continue: consumes the parent, notes read "continued from".
	resCont, err := ContinueRetainedRun(context.Background(), rt, fork.ProposalID, 2, "", true)
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	cont := decodeProposalEnvelope(t, resCont)
	if !strings.HasPrefix(cont.Notes, "continued from ") || !strings.Contains(cont.Notes, "original discarded") {
		t.Errorf("continue notes = %q, want a consuming 'continued from' note", cont.Notes)
	}
}

// TestRetainedStopReason pins the typed stop classification, including the
// distinctions the envelope's error text already makes: a provider-internal
// deadline is not a role-budget timeout, and a truncated finish is max_tokens.
func TestRetainedStopReason(t *testing.T) {
	tests := []struct {
		name    string
		stop    *jobs.SubAgentStop
		bodyErr error
		source  retainedFailureSource
		want    string
	}{
		{name: "completed", want: "completed"},
		{name: "turn budget", stop: &jobs.SubAgentStop{TurnBudgetExhausted: true}, want: "turn_budget"},
		{name: "iteration budget", stop: &jobs.SubAgentStop{IterationBudgetExhausted: true}, want: "iteration_budget"},
		{name: "max tokens", stop: &jobs.SubAgentStop{FinishReason: "length"}, want: "max_tokens"},
		{name: "halted", stop: &jobs.SubAgentStop{HaltedBySupervisor: true}, want: "halted"},
		{name: "role budget deadline", bodyErr: context.DeadlineExceeded, source: retainedFailureBudget, want: "timeout"},
		{name: "parent deadline", bodyErr: context.DeadlineExceeded, source: retainedFailureParent, want: "timeout"},
		{name: "provider deadline", bodyErr: context.DeadlineExceeded, source: retainedFailureProvider, want: "provider_error"},
		{name: "canceled", bodyErr: context.Canceled, source: retainedFailureProvider, want: "canceled"},
		// LastFinishReason is stale across a failed final call; the terminal
		// error must win over an earlier "length".
		{name: "stale length does not mask timeout", stop: &jobs.SubAgentStop{FinishReason: "length"}, bodyErr: context.DeadlineExceeded, source: retainedFailureBudget, want: "timeout"},
		{name: "stale length does not mask cancel", stop: &jobs.SubAgentStop{FinishReason: "length"}, bodyErr: context.Canceled, source: retainedFailureProvider, want: "canceled"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := retainedStopReason(tc.stop, tc.bodyErr, tc.source); got != tc.want {
				t.Errorf("retainedStopReason(...) = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRetained_ContinuationPromptCarriesWorkspaceRoot: a re-run prompt names the
// live workspace root so the model does not echo a prior run's absolute path.
func TestRetained_ContinuationPromptCarriesWorkspaceRoot(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	seed := []types.Message{types.UserMsg("go")}
	var calls int
	var seenPrompt, seenWorkdir string
	rt := RetainedRuntime{
		Runner: func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
			calls++
			if calls > 1 {
				seenPrompt, seenWorkdir = prompt, params.Workdir
			}
			writeTestFile(t, params.Workdir, "work.txt", "work\n")
			jobs.CaptureSubAgentTranscript(ctx, seed)
			return "done", nil
		},
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}

	res, err := StartRetainedRun(context.Background(), rt, "go", SubAgentParams{Role: "developer"}, 10*time.Second, false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	env := decodeProposalEnvelope(t, res)
	if _, err := ContinueRetainedRun(context.Background(), rt, env.ProposalID, 2, "", false); err != nil {
		t.Fatalf("continue: %v", err)
	}
	if seenWorkdir == "" || !strings.Contains(seenPrompt, seenWorkdir) {
		t.Errorf("continuation prompt %q does not name the workspace root %q", seenPrompt, seenWorkdir)
	}
}

// TestProposalEnvelope_NextAdvertisesContinuation: `next` is a real eligibility
// list — an open worktree proposal with a captured conversation advertises the
// re-run verbs, on the dispatch envelope and on `show`.
func TestProposalEnvelope_NextAdvertisesContinuation(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	seed := []types.Message{types.UserMsg("do it")}
	rt := RetainedRuntime{
		Runner: func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
			writeTestFile(t, params.Workdir, "work.txt", "work\n")
			jobs.CaptureSubAgentTranscript(ctx, seed)
			return "done", nil
		},
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}

	res, err := StartRetainedRun(context.Background(), rt, "go", SubAgentParams{Role: "developer"}, 10*time.Second, false)
	if err != nil {
		t.Fatalf("StartRetainedRun: %v", err)
	}
	env := decodeProposalEnvelope(t, res)
	for _, want := range []string{"continue", "fork"} {
		if !slices.Contains(env.Next, want) {
			t.Errorf("dispatch next = %v, want it to include %q", env.Next, want)
		}
	}

	out, err := (&ProposalTool{Proposals: rt.Proposals}).Execute(context.Background(),
		`{"action":"show","proposal_id":`+jsonString(env.ProposalID)+`}`)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	shown := decodeProposalEnvelope(t, out)
	if !slices.Contains(shown.Next, "continue") || !slices.Contains(shown.Next, "fork") {
		t.Errorf("show next = %v, want it to include continue and fork", shown.Next)
	}
}

// TestProposalRegistry_ClaimContinueSerializes: a second continue/fork on the
// same proposal is refused while one is in flight.
func TestProposalRegistry_ClaimContinueSerializes(t *testing.T) {
	r := NewProposalRegistry()
	if !r.claimContinue("p1") {
		t.Fatal("first claim should succeed")
	}
	if r.claimContinue("p1") {
		t.Fatal("second claim should be refused")
	}
	r.releaseContinue("p1")
	if !r.claimContinue("p1") {
		t.Fatal("claim after release should succeed")
	}
}
