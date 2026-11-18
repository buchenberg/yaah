package host

// replay_host_test.go pins the replay equivalence at the composition that
// produced the records (yaah-0u0v.25): a run over the real retained
// machinery — a real temp git repo, a real worktree, a real settlement — is
// recorded into a state database, and the same graph replayed through
// internal/workflow/replay's door reproduces the run's node outcomes and its
// settlement with no runner configured at all, which is the zero-model-calls
// acceptance made structural rather than promised.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/workflow/exec"
	"github.com/buchenberg/yaah/internal/workflow/replay"
	"github.com/buchenberg/yaah/internal/workflow/store"
)

// replayProjection is what an honest replay must reproduce: the run's
// disposition and, per node, its state, its recorded value, and its
// dispositions. Two limits are stated rather than hidden:
//
//   - a settle node's structured Result renders lane-attributed facts (the
//     producer path the envelope named) the settle records deliberately do
//     not duplicate, so its VALUE is excluded and its dispositions are
//     compared field-wise instead — outcome equal, attribution compared
//     where it is recorded (on the disposition itself);
//   - attempt history is not part of the claim — a replay re-folds the same
//     answers, and an answer the original slept on its way to is still one
//     answer here — and neither are door-side attribution facts (which
//     model, how long), which belong to the door that produced them.
type replayProjection struct {
	status string
	reason string
	nodes  map[string]nodeProjection
}

type nodeProjection struct {
	state    string
	reason   string
	result   string
	kind     string
	settled  []exec.Settled
	discards []exec.Settled
}

func projectRun(res exec.Result) replayProjection {
	p := replayProjection{
		status: string(res.Status),
		reason: res.Reason,
		nodes:  map[string]nodeProjection{},
	}
	for _, n := range res.Nodes {
		proj := nodeProjection{
			state:    string(n.State),
			reason:   n.Reason,
			kind:     string(n.Kind),
			settled:  n.Settled,
			discards: n.Discards,
		}
		// A settle node's structured Result renders lane-attributed facts the
		// settle records deliberately do not duplicate (the producer path the
		// envelope named); its dispositions are compared field-wise instead.
		if string(n.Kind) != "settle" {
			proj.result = n.Result
		}
		p.nodes[n.Path] = proj
	}
	return p
}

// TestReplayReproducesRecordedRunWithNoRunner is the acceptance path: the
// settled chain runs, its checkpoint is loaded, and the same graph executes
// against the replay door — with Runner nil, so a model call is unreachable
// by construction. Node outcomes, the recorded values, the settled
// dispositions (same proposal id, same verb, same status), and the run's
// disposition are the recorded run's; the world is not touched, so the
// select that landed the file the first time does not land it again.
func TestReplayReproducesRecordedRunWithNoRunner(t *testing.T) {
	env := retainedEnv(t, writeFileRunner("change.txt", "from the sub-agent\n"))
	y := strings.Replace(specHead, "%s", "replay-settle", 1) + settleChainSpec
	g := graphFromYAML(t, y)

	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := memory.Open(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer db.Close()
	st, err := store.New(db)
	if err != nil {
		t.Fatalf("state store: %v", err)
	}

	original, xerr := Execute(context.Background(), Options{
		Graph:    g,
		RunID:    "replay-src",
		Runner:   env.Runner,
		Retained: env,
		Store:    st,
	})
	if xerr != nil {
		t.Fatalf("Execute (recording run): %v", xerr)
	}
	if original.Run.Status != exec.RunSucceeded {
		t.Fatalf("status = %s reason=%q, want the recording run succeeded\nlog:\n%s",
			original.Run.Status, original.Run.Reason, logOf(original.Run))
	}

	cp, lerr := st.LoadCheckpoint(context.Background(), "replay-src")
	if lerr != nil {
		t.Fatalf("LoadCheckpoint: %v", lerr)
	}
	door, derr := replay.New(cp)
	if derr != nil {
		t.Fatalf("replay.New: %v", derr)
	}

	// No Runner and no Retained: the replay door is the whole world. A model
	// call is unreachable, and a settle asks the records, not the lane — so
	// nothing lands on the host repo a second time.
	replayed, xerr := Execute(context.Background(), Options{
		Graph: g,
		RunID: "replay-of-replay-src",
		Door:  door,
	})
	if xerr != nil {
		t.Fatalf("Execute (replay): %v\nlog:\n%s", xerr, logOf(replayed.Run))
	}

	assertSameProjection(t, projectRun(original.Run), projectRun(replayed.Run))

	work := nodeState(t, original.Run, "steps.work")
	land := nodeState(t, original.Run, "steps.land")
	replayWork := nodeState(t, replayed.Run, "steps.work")
	replayLand := nodeState(t, replayed.Run, "steps.land")

	// The settlement is the same fact: one select of the same proposal, and
	// the ledger the second run held is empty because the first disposition
	// closed it.
	if len(replayLand.Settled) != 1 || len(land.Settled) != 1 {
		t.Fatalf("settled: original %+v, replay %+v — want one disposition each", land.Settled, replayLand.Settled)
	}
	if replayLand.Settled[0].ProposalID != land.Settled[0].ProposalID ||
		replayLand.Settled[0].Action != "select" ||
		replayLand.Settled[0].Status != land.Settled[0].Status {
		t.Errorf("replayed settlement = %+v, want the recorded %+v", replayLand.Settled[0], land.Settled[0])
	}
	if replayWork.Proposal == nil || work.Proposal == nil ||
		replayWork.Proposal.ProposalID != work.Proposal.ProposalID {
		t.Errorf("replayed work node sealed %+v, want the recorded handle %+v", replayWork.Proposal, work.Proposal)
	}

	// The world is not re-performed: the file the first select landed was
	// never deleted by a replay, and a replay that re-selected would have
	// asked the lane — which this composition does not hold.
	if _, err := os.Stat(filepath.Join(env.RepoPath, "change.txt")); err != nil {
		t.Errorf("the recording run's landed file must survive the replay: %v", err)
	}
	if replayed.Runtime != nil {
		t.Errorf("replay result holds a dispatch runtime; a replay composes none")
	}
}

func assertSameProjection(t *testing.T, want, got replayProjection) {
	t.Helper()
	if want.status != got.status || want.reason != got.reason {
		t.Errorf("run = %s (%q), want %s (%q)", got.status, got.reason, want.status, want.reason)
	}
	if len(want.nodes) != len(got.nodes) {
		t.Fatalf("node count = %d, want %d", len(got.nodes), len(want.nodes))
	}
	for path, wn := range want.nodes {
		gn, ok := got.nodes[path]
		if !ok {
			t.Fatalf("node %s missing from the replay's projection", path)
		}
		if wn.state != gn.state || wn.reason != gn.reason || wn.result != gn.result {
			t.Errorf("node %s = %s (%q), want %s (%q)", path, gn.state, gn.reason, wn.state, wn.reason)
		}
		if len(wn.settled) != len(gn.settled) || len(wn.discards) != len(gn.discards) {
			t.Fatalf("node %s dispositions: settled %d/%d discards %d/%d",
				path, len(gn.settled), len(wn.settled), len(gn.discards), len(wn.discards))
		}
		for i := range wn.settled {
			if wn.settled[i].ProposalID != gn.settled[i].ProposalID ||
				wn.settled[i].Action != gn.settled[i].Action ||
				wn.settled[i].Status != gn.settled[i].Status ||
				wn.settled[i].LeftOpen != gn.settled[i].LeftOpen ||
				strings.Join(wn.settled[i].Files, ",") != strings.Join(gn.settled[i].Files, ",") {
				t.Errorf("node %s settled[%d] = %+v, want %+v", path, i, gn.settled[i], wn.settled[i])
			}
		}
		for i := range wn.discards {
			if wn.discards[i].ProposalID != gn.discards[i].ProposalID {
				t.Errorf("node %s discard[%d] = %+v, want %+v", path, i, gn.discards[i], wn.discards[i])
			}
		}
	}
}
