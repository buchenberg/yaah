package dispatch

// retained_durable_test.go covers durable worktree proposals: after a simulated
// restart the kernel recovers the scope + sealed output from the trace, and the
// sidecar restores the result, transcript, and params so the proposal is
// listable, settle-able, and continuable.

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/agent/recovery"
	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/types"
)

// TestRetained_RehydrateScopesBySession pins the session boundary: a sidecar
// from another session is not surfaced even when it lives in the same repo.
func TestRetained_RehydrateScopesBySession(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	seed := []types.Message{types.UserMsg("go")}
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
		SessionID:    "sess-a",
		Proposals:    NewProposalRegistry(),
	}

	res, err := StartRetainedRun(context.Background(), rt, "go", SubAgentParams{Role: "developer"}, 10*time.Second, false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	env := decodeProposalEnvelope(t, res)

	newMgr, _, rerr := recovery.Recover(context.Background(), fx.store, repo, true, nil)
	if rerr != nil {
		t.Fatalf("recover: %v", rerr)
	}
	regB, _ := LoadProposalRegistry(repo, newMgr, rootID, "sess-b")
	if _, err := regB.get(env.ProposalID); err == nil {
		t.Error("another session rehydrated the proposal")
	}
	regA, _ := LoadProposalRegistry(repo, newMgr, rootID, "sess-a")
	if _, err := regA.get(env.ProposalID); err != nil {
		t.Errorf("owning session did not rehydrate the proposal: %v", err)
	}
}

func TestRetained_WorktreeProposalSurvivesRestart(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	transcript := []types.Message{types.SystemMsg("dev sys"), types.UserMsg("do the work")}
	var calls int
	var gotSeed []types.Message

	rt := RetainedRuntime{
		Runner: func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
			calls++
			if calls == 1 {
				writeTestFile(t, params.Workdir, "work.txt", "work\n")
				jobs.CaptureSubAgentTranscript(ctx, transcript)
				return "done", nil
			}
			gotSeed = params.SeedMessages
			writeTestFile(t, params.Workdir, "more.txt", "more\n")
			return "continued", nil
		},
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		SessionID:    "sess-durable",
		Proposals:    NewProposalRegistry(),
	}

	res, err := StartRetainedRun(context.Background(), rt, "go", SubAgentParams{Role: "developer"}, 10*time.Second, false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	env := decodeProposalEnvelope(t, res)
	if env.Status != "proposal" {
		t.Fatalf("dispatch status = %q (%s)", env.Status, env.Error)
	}
	live, err := rt.Proposals.get(env.ProposalID)
	if err != nil {
		t.Fatalf("live proposal: %v", err)
	}
	wantDiff := live.reviewDiff()
	if wantDiff == "" {
		t.Fatalf("sealed proposal has no diff to persist")
	}

	// Simulate a restart: rebuild the scope tree and sealed outputs from the
	// durable trace, then rehydrate the proposal registry from that manager.
	newMgr, _, rerr := recovery.Recover(context.Background(), fx.store, repo, true, nil)
	if rerr != nil {
		t.Fatalf("recover: %v", rerr)
	}
	reg, warnings := LoadProposalRegistry(repo, newMgr, rootID, "sess-durable")
	p, err := reg.get(env.ProposalID)
	if err != nil {
		t.Fatalf("recovered proposal %s missing: %v (warnings: %v)", env.ProposalID, err, warnings)
	}
	if !reflect.DeepEqual(p.transcript, transcript) {
		t.Errorf("recovered transcript = %+v, want %+v", p.transcript, transcript)
	}
	if p.result != "done" {
		t.Errorf("recovered result = %q, want %q", p.result, "done")
	}
	if p.params.Role != "developer" {
		t.Errorf("recovered params role = %q, want developer", p.params.Role)
	}
	// The kernel seal payload omits the diff; the sidecar must restore it so a
	// recovered proposal still shows and releases its change.
	if p.reviewDiff() != wantDiff {
		t.Errorf("recovered diff = %q, want %q", p.reviewDiff(), wantDiff)
	}

	// The recovered proposal is continuable: the continuation seeds from the
	// recovered workspace and transcript.
	newRT := RetainedRuntime{
		Runner:       rt.Runner,
		RepoPath:     repo,
		ScopeManager: newMgr,
		RootScopeID:  rootID,
		WorktreeRoot: rt.WorktreeRoot,
		SessionID:    "sess-durable",
		Proposals:    reg,
	}
	res2, err := ContinueRetainedRun(context.Background(), newRT, env.ProposalID, 2, "", false)
	if err != nil {
		t.Fatalf("continue after restart: %v", err)
	}
	if env2 := decodeProposalEnvelope(t, res2); env2.Status != "proposal" {
		t.Fatalf("continuation status = %q (%s)", env2.Status, env2.Error)
	}
	if !reflect.DeepEqual(gotSeed, transcript) {
		t.Errorf("continuation seed after restart = %+v, want %+v", gotSeed, transcript)
	}
}
