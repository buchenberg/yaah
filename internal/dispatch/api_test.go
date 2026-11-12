package dispatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/jobs"
)

// TestRunSharedLaneReturnsTypedResult proves the executor-facing core can run a
// shared dispatch without the delegate tool, and that it reports a typed
// completed result rather than a model-facing JSON frame.
func TestRunSharedLaneReturnsTypedResult(t *testing.T) {
	var gotRole, gotPrompt string
	runner := func(ctx context.Context, prompt string, sp SubAgentParams) (string, error) {
		gotRole = sp.Role
		gotPrompt = prompt
		return "the report", nil
	}

	res, err := Run(context.Background(), DispatchRequest{
		Lane:   LaneShared,
		Params: SubAgentParams{Role: "worker"},
		Prompt: "do the work",
		Runner: runner,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != DispatchCompleted {
		t.Fatalf("status = %q, want %q", res.Status, DispatchCompleted)
	}
	if res.Result != "the report" {
		t.Fatalf("result = %q, want %q", res.Result, "the report")
	}
	if gotRole != "worker" || gotPrompt != "do the work" {
		t.Fatalf("runner saw role=%q prompt=%q", gotRole, gotPrompt)
	}
}

// TestRunSharedLaneClassifiesStuck proves a stuck child is a typed status, not
// a formatted result the executor would have to parse.
func TestRunSharedLaneClassifiesStuck(t *testing.T) {
	runner := func(context.Context, string, SubAgentParams) (string, error) {
		return "partial output", jobs.ErrStuckChild
	}

	res, err := Run(context.Background(), DispatchRequest{
		Lane:   LaneShared,
		Params: SubAgentParams{Role: "worker"},
		Prompt: "do the work",
		Runner: runner,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != DispatchStuck {
		t.Fatalf("status = %q, want %q", res.Status, DispatchStuck)
	}
	if res.Partial != "partial output" {
		t.Fatalf("partial = %q, want %q", res.Partial, "partial output")
	}
}

// TestRunSharedLaneClassifiesParentTimeout covers the deadline path the tool
// used to classify itself.
func TestRunSharedLaneClassifiesParentTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	runner := func(ctx context.Context, _ string, _ SubAgentParams) (string, error) {
		<-ctx.Done()
		return "partial output", ctx.Err()
	}

	res, err := Run(ctx, DispatchRequest{
		Lane:   LaneShared,
		Params: SubAgentParams{Role: "worker"},
		Prompt: "do the work",
		Runner: runner,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != DispatchTimedOut {
		t.Fatalf("status = %q, want %q", res.Status, DispatchTimedOut)
	}
}

// TestRunBackgroundRequiresManager keeps the core's guard explicit: a caller
// that asks for background work without the job manager gets a clear error.
func TestRunBackgroundRequiresManager(t *testing.T) {
	runner := func(context.Context, string, SubAgentParams) (string, error) {
		return "unused", nil
	}
	_, err := Run(context.Background(), DispatchRequest{
		Lane:       LaneShared,
		Params:     SubAgentParams{Role: "worker"},
		Prompt:     "do the work",
		Background: true,
		Runner:     runner,
	})
	if err == nil || !strings.Contains(err.Error(), "background mode requested but not available") {
		t.Fatalf("err = %v, want the background-manager error", err)
	}
}

// TestRunProposalLaneReturnsTypedProposal proves the retained proposal lane is
// reachable from the core and yields a typed envelope, not a JSON string.
func TestRunProposalLaneReturnsTypedProposal(t *testing.T) {
	rt := newRetainedTestEnv(t, writingRunner(t, "out.txt"))

	res, err := Run(context.Background(), DispatchRequest{
		Lane:     LaneProposal,
		Params:   SubAgentParams{Role: "worker"},
		Prompt:   "do the work",
		Retained: &rt,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != DispatchProposal {
		t.Fatalf("status = %q, want %q", res.Status, DispatchProposal)
	}
	if res.Proposal == nil {
		t.Fatal("proposal lane must return a typed envelope")
	}
	if res.Proposal.Status != "proposal" || res.Proposal.ProposalID == "" {
		t.Fatalf("proposal = %+v, want a sealed proposal", res.Proposal)
	}
}

// TestSettleSelectDirect proves settlement is callable without the proposal
// tool and returns the typed settlement struct.
func TestSettleSelectDirect(t *testing.T) {
	rt := newRetainedTestEnv(t, writingRunner(t, "out.txt"))

	res, err := StartRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "worker"}, 0, false)
	if err != nil {
		t.Fatalf("StartRetainedRun: %v", err)
	}
	env := decodeProposalEnvelope(t, res)
	if env.Status != "proposal" || env.ProposalID == "" {
		t.Fatalf("want a sealed proposal, got %+v", env)
	}

	settlement, err := Settle(context.Background(), rt.Proposals, env.ProposalID, SettleSelect, SettleOpts{})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if settlement.Status != "settled" || settlement.Action != SettleSelect {
		t.Fatalf("settlement = %+v, want settled/select", settlement)
	}
	if settlement.Outcome.Work != "worktree landed" {
		t.Fatalf("typed outcome = %+v, want the select work description", settlement.Outcome)
	}
	if settlement.Envelope().Outcome != settlement.Outcome.String() {
		t.Fatalf("envelope outcome = %q, want %q", settlement.Envelope().Outcome, settlement.Outcome.String())
	}
	if _, err := os.Stat(filepath.Join(rt.RepoPath, "out.txt")); err != nil {
		t.Fatalf("select must land the work on the host: %v", err)
	}
}

// TestSettleUnknownProposalDirect keeps the core's error path typed: a missing
// proposal is an error, not an envelope the caller must inspect.
func TestSettleUnknownProposalDirect(t *testing.T) {
	rt := newRetainedTestEnv(t, writingRunner(t, "out.txt"))

	_, err := Settle(context.Background(), rt.Proposals, "proposal:missing", SettleSelect, SettleOpts{})
	if err == nil {
		t.Fatal("Settle must reject an unknown proposal")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected cancellation: %v", err)
	}
}
