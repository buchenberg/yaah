package dispatch

import (
	"context"
	"reflect"
	"slices"
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
// captured no conversation (report/older run) cannot be continued — the tool
// fails loudly instead of silently starting cold.
func TestRetained_ContinueRefusesWithoutConversation(t *testing.T) {
	rt := RetainedRuntime{
		Runner:       func(context.Context, string, SubAgentParams) (string, error) { return "", nil },
		Proposals:    NewProposalRegistry(),
		RootScopeID:  "scope:missing",
		ScopeManager: nil,
	}
	if _, err := ContinueRetainedRun(context.Background(), rt, "proposal:nope", 1, "", true); err == nil {
		t.Fatal("expected an error for an unknown proposal")
	}
}
