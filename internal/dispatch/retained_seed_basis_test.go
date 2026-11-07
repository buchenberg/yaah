package dispatch

// Seed-basis regressions for continue/fork/seed_from (yaah-9m0c), plus the
// substrate migration (yaah-hxk3.8): lineage, review-pair recovery, and the
// absence of a proposal sidecar.

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	goatherd "github.com/buchenberg/goatherd-runtime"

	"github.com/buchenberg/yaah/internal/agent/recovery"
	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/types"
)

// TestRetained_ThreadedDeltaIgnoresHostDrift pins yaah-9m0c: a continuation
// or seed_from dispatch seeded from a proposal must compute its delta against
// the proposal's recorded basis, not the host tree at continuation time. A
// sibling commit or uncommitted file landing in between must not appear as a
// phantom deletion/reversion in the new envelope.
func TestRetained_ThreadedDeltaIgnoresHostDrift(t *testing.T) {
	cases := []struct {
		name    string
		drift   func(t *testing.T, repo string)
		contend func(t *testing.T, rt *RetainedRuntime, proposalID string) string
	}{
		{
			name: "continue over a committed sibling",
			drift: func(t *testing.T, repo string) {
				writeTestFile(t, repo, "sibling.txt", "sibling\n")
				mustRunGit(t, repo, "add", "-A")
				mustRunGit(t, repo, "commit", "-m", "sibling landed")
			},
			contend: func(t *testing.T, rt *RetainedRuntime, proposalID string) string {
				res, err := ContinueRetainedRun(context.Background(), *rt, proposalID, 2, "", true)
				if err != nil {
					t.Fatalf("continue: %v", err)
				}
				return decodeProposalEnvelope(t, res).ProposalID
			},
		},
		{
			name: "continue over an uncommitted sibling",
			drift: func(t *testing.T, repo string) {
				writeTestFile(t, repo, "sibling.txt", "sibling\n")
			},
			contend: func(t *testing.T, rt *RetainedRuntime, proposalID string) string {
				res, err := ContinueRetainedRun(context.Background(), *rt, proposalID, 2, "", true)
				if err != nil {
					t.Fatalf("continue: %v", err)
				}
				return decodeProposalEnvelope(t, res).ProposalID
			},
		},
		{
			name: "fork over an uncommitted sibling",
			drift: func(t *testing.T, repo string) {
				writeTestFile(t, repo, "sibling.txt", "sibling\n")
			},
			contend: func(t *testing.T, rt *RetainedRuntime, proposalID string) string {
				res, err := ContinueRetainedRun(context.Background(), *rt, proposalID, 2, "", false)
				if err != nil {
					t.Fatalf("fork: %v", err)
				}
				return decodeProposalEnvelope(t, res).ProposalID
			},
		},
		{
			name: "seed_from over an uncommitted sibling",
			drift: func(t *testing.T, repo string) {
				writeTestFile(t, repo, "sibling.txt", "sibling\n")
			},
			contend: func(t *testing.T, rt *RetainedRuntime, proposalID string) string {
				res, err := StartRetainedRun(context.Background(), *rt, "seed again", SubAgentParams{
					Role:     "developer",
					SeedFrom: proposalID,
				}, 10*time.Second, false)
				if err != nil {
					t.Fatalf("seed_from: %v", err)
				}
				return decodeProposalEnvelope(t, res).ProposalID
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx, repo, rootID := newSessionFixtures(t)
			var calls int
			rt := RetainedRuntime{
				Runner: func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
					calls++
					if calls == 1 {
						writeTestFile(t, params.Workdir, "p.txt", "parent work\n")
						jobs.CaptureSubAgentTranscript(ctx, []types.Message{types.UserMsg("do the work")})
						return "parent done", nil
					}
					writeTestFile(t, params.Workdir, "c.txt", "follow-up work\n")
					return "follow-up done", nil
				},
				RepoPath:     repo,
				ScopeManager: fx.mgr,
				RootScopeID:  rootID,
				WorktreeRoot: t.TempDir(),
				Proposals:    NewProposalRegistry(),
			}

			res, err := StartRetainedRun(context.Background(), rt, "do the work", SubAgentParams{Role: "developer"}, 10*time.Second, false)
			if err != nil {
				t.Fatalf("initial dispatch: %v", err)
			}
			orig := decodeProposalEnvelope(t, res)
			if orig.Status != "proposal" {
				t.Fatalf("initial status = %q (%s)", orig.Status, orig.Error)
			}

			tc.drift(t, repo)

			nextID := tc.contend(t, &rt, orig.ProposalID)
			next, err := rt.Proposals.get(nextID)
			if err != nil {
				t.Fatalf("next proposal missing: %v", err)
			}

			// The envelope lists the source's work plus this run's edits, and
			// nothing the sibling changed.
			want := []string{"c.txt", "p.txt"}
			got := changesPaths(next.output.Changes())
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Errorf("delta files = %v, want %v (host sibling must not leak in)", got, want)
			}
			for _, change := range next.output.Changes() {
				if change.Path == "sibling.txt" {
					t.Errorf("delta carries the host sibling %q as %s", change.Path, change.Kind)
				}
			}
		})
	}
}

// TestRetained_NoProposalSidecar pins the yaah-hxk3.8 sidecar removal: the
// sealed output is the durable record.
func TestRetained_NoProposalSidecar(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	rt := RetainedRuntime{
		Runner: func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
			writeTestFile(t, params.Workdir, "work.txt", "work\n")
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
	p, err := rt.Proposals.get(env.ProposalID)
	if err != nil {
		t.Fatalf("proposal: %v", err)
	}
	if _, err := os.Stat(p.workdir + ".proposal.json"); !os.IsNotExist(err) {
		t.Errorf("proposal sidecar still exists at %s.proposal.json: %v", p.workdir, err)
	}
	sidecars, _ := filepath.Glob(filepath.Join(filepath.Dir(p.workdir), "*.proposal.json"))
	if len(sidecars) != 0 {
		t.Errorf("found proposal sidecars after seal: %v", sidecars)
	}
}

// TestRetained_ContinuationLineageAndReviewRecovery pins the native lineage
// edges and the review pair across a restart: a continuation derives from and
// supersedes its source, the source reports SupersededBy, and the recovered
// proposal can still record its settlement decision.
func TestRetained_ContinuationLineageAndReviewRecovery(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	var calls int
	rt := RetainedRuntime{
		Runner: func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
			calls++
			if calls == 1 {
				writeTestFile(t, params.Workdir, "p.txt", "parent\n")
				jobs.CaptureSubAgentTranscript(ctx, []types.Message{types.UserMsg("go")})
				return "parent", nil
			}
			writeTestFile(t, params.Workdir, "c.txt", "child\n")
			return "child", nil
		},
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		SessionID:    "sess-lineage",
		Proposals:    NewProposalRegistry(),
	}

	res, err := StartRetainedRun(context.Background(), rt, "go", SubAgentParams{Role: "developer"}, 10*time.Second, false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	orig := decodeProposalEnvelope(t, res)
	origProposal, err := rt.Proposals.get(orig.ProposalID)
	if err != nil {
		t.Fatalf("original proposal: %v", err)
	}

	res2, err := ContinueRetainedRun(context.Background(), rt, orig.ProposalID, 2, "", true)
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	cont := decodeProposalEnvelope(t, res2)
	contProposal, err := rt.Proposals.get(cont.ProposalID)
	if err != nil {
		t.Fatalf("continuation proposal: %v", err)
	}

	if got := contProposal.output.DerivedFrom(); got != origProposal.output.ID() {
		t.Errorf("continuation DerivedFrom = %q, want %q", got, origProposal.output.ID())
	}
	if got := contProposal.output.Supersedes(); got != origProposal.output.ID() {
		t.Errorf("continuation Supersedes = %q, want %q", got, origProposal.output.ID())
	}
	if got := origProposal.output.SupersededBy(); got != contProposal.output.ID() {
		t.Errorf("source SupersededBy = %q, want %q", got, contProposal.output.ID())
	}

	// Restart: recovery rebuilds the output lineage and the review pair; the
	// proposal registry is rebuilt from the substrate, not a sidecar.
	newMgr, _, rerr := recovery.Recover(context.Background(), fx.store, repo, true, nil)
	if rerr != nil {
		t.Fatalf("recover: %v", rerr)
	}
	reg, warnings := LoadProposalRegistry(repo, newMgr, rootID, "sess-lineage")
	recovered, err := reg.get(cont.ProposalID)
	if err != nil {
		t.Fatalf("recovered proposal missing: %v (warnings: %v)", err, warnings)
	}
	if recovered.reviewProposal == nil || recovered.reviewFactID == "" {
		t.Fatalf("recovered proposal lost its review pair: proposal=%v fact=%q", recovered.reviewProposal, recovered.reviewFactID)
	}
	if got := recovered.output.DerivedFrom(); got != origProposal.output.ID() {
		t.Errorf("recovered DerivedFrom = %q, want %q", got, origProposal.output.ID())
	}
	if got := recovered.output.Supersedes(); got != origProposal.output.ID() {
		t.Errorf("recovered Supersedes = %q, want %q", got, origProposal.output.ID())
	}
	if sourceOut, ok := newMgr.Output(origProposal.output.ID()); !ok || sourceOut.SupersededBy() != recovered.output.ID() {
		t.Errorf("recovered source SupersededBy = %v, want %q", sourceOut, recovered.output.ID())
	}

	// The recovered pair is enough to record the decision after restart.
	if err := recovered.recordReviewDecision(context.Background(), goatherd.SettleRelease, nil); err != nil {
		t.Fatalf("record decision after recovery: %v", err)
	}
	review, ok := newMgr.MergeReview(recovered.scopeID)
	if !ok || review.Decision == nil {
		t.Fatalf("recovered review = %+v, want a recorded decision", review)
	}
}
