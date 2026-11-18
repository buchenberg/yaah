package exec

// resume_property_test.go pins the resume idempotency property
// (yaah-0u0v.25): whatever sequence of process deaths a run suffers, the
// survives-and-converges stories hold —
//
//	P1  a run that finishes succeeded produced the same node outcomes,
//	    values, and dispositions as the uninterrupted run;
//	P2  no proposal is ever asked for a second time — across ALL lives the
//	    door saw at most one settle call per proposal and verb;
//	P3  no attempt number is folded twice — the final attempt history is
//	    strictly unique per node;
//	P4  when the crash took a settlement's answer with it, the run says so:
//	    it ends without success, its reason names the interrupted
//	    settlement, and the proposal is still open — the write-ahead
//	    discipline's refusal to re-ask is the outcome, and a schedule that
//	    hits that window converges ON the honesty, not past it.
//
// Rather than a curated kill point (persist_test's sweeps already pin every
// boundary once), the schedule here is SEEDED RANDOM: a bounded spread of
// process deaths at seeded distances, and survival after the last one. The
// invariants must hold for any schedule at all, which is what makes this a
// property and not a walk through the same cases again.

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// propertyGraph is one run shape under test.
type propertyGraph struct {
	g      *ir.Graph
	script map[string][]Completion
	// honestFailure names the one window this graph may converge on without
	// success: a settlement ask whose answer the crash took. Graphs without
	// a settle node have no such window, and ANY non-success resumption of
	// them is a bug this property must name.
	honestFailure bool
}

func propertyGraphs(t *testing.T) map[string]propertyGraph {
	t.Helper()
	retryDoc := strings.Replace(specHead, "%s", "prop-retry", 1) + `  steps:
    - id: flaky
      kind: delegate
      role: developer
      prompt: do the flaky thing
      # The ceiling is tall on purpose: a fresh door replays the scripted
      # failure to every re-dispatch a crash caused, so the budget's variable
      # here is the schedule's kill depth, not the retry count a real lane
      # would have consumed.
      retry: {max_attempts: 14}
    - id: after
      kind: delegate
      role: tester
      needs: [flaky]
      prompt: check the flaky output
`
	fanoutDoc := strings.Replace(specHead, "%s", "prop-fanout", 1) + `  steps:
    - id: f
      kind: fanout
      over: [a, b, c]
      as: ws
      body:
        kind: delegate
        role: developer
        prompt: "work {{ ws }}"
      collect: results
`
	return map[string]propertyGraph{
		"settle-chain": {
			g: graphFromYAML(t, settleChain("prop-settle", "select")),
			script: map[string][]Completion{
				"steps.work": {Proposal("prop-1", 0)},
			},
			honestFailure: true,
		},
		"retry-then-report": {
			g: graphFromYAML(t, retryDoc),
			script: map[string][]Completion{
				"steps.flaky": {
					Failure("provider_error", "a retryable failure", 0),
					Report("the report", 0),
				},
				"steps.after": {Report("checked", 0)},
			},
		},
		"fanout-join-all": {
			g: graphFromYAML(t, fanoutDoc),
			script: map[string][]Completion{
				itemPathOf("steps.f", 0, 0): {Report("v0", 0)},
				itemPathOf("steps.f", 1, 0): {Report("v1", 0)},
				itemPathOf("steps.f", 2, 0): {Report("v2", 0)},
			},
		},
	}
}

// outcomeProjection is the converged state a resumed run must reproduce: the
// run's disposition plus per node its lifecycle state, its recorded value,
// and its dispositions. Attempt history is excluded on purpose: a crash may
// re-dispatch an attempt the first life never answered, which is correct
// resume behavior and not a divergence.
type outcomeProjection struct {
	status string
	reason string
	nodes  map[string]projectedNode
}

type projectedNode struct {
	state    string
	result   string
	disposed string
}

func project(t *testing.T, res Result) outcomeProjection {
	t.Helper()
	p := outcomeProjection{status: string(res.Status), reason: res.Reason, nodes: map[string]projectedNode{}}
	paths := make([]string, 0, len(res.Nodes))
	for _, n := range res.Nodes {
		paths = append(paths, n.Path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		n := nodeOf(t, res, path)
		var disposed []string
		for _, s := range n.Settled {
			disposed = append(disposed, s.ProposalID+" "+s.Action+" "+s.Status)
		}
		for _, s := range n.Discards {
			disposed = append(disposed, s.ProposalID+" discarded")
		}
		p.nodes[path] = projectedNode{
			state:    string(n.State),
			result:   n.Result,
			disposed: strings.Join(disposed, ","),
		}
	}
	return p
}

// TestResumePropertyConvergesUnderRandomKillSchedules runs each graph shape
// under several seeded random kill schedules and asserts the property: at
// most one settle ask per proposal and verb across all lives (P2), unique
// attempt ordinals in the final history (P3), and the convergence/honesty
// split of P1 and P4.
func TestResumePropertyConvergesUnderRandomKillSchedules(t *testing.T) {
	for name, pg := range propertyGraphs(t) {
		t.Run(name, func(t *testing.T) {
			// The uninterrupted baseline the property measures against.
			base, baseFake := runGraph(t, pg.g, pg.script, Config{})
			if base.Status != RunSucceeded {
				t.Fatalf("baseline status = %s (%q) — the property needs a completable graph", base.Status, base.Reason)
			}
			want := project(t, base)
			var baseAsks []string
			for _, req := range baseFake.Settles() {
				baseAsks = append(baseAsks, req.ProposalID+" "+req.Verb)
			}
			sort.Strings(baseAsks)

			for seed := int64(0); seed < 6; seed++ {
				t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
					rnd := rand.New(rand.NewSource(seed))
					store := &jsonStore{}
					// The schedule is a bounded spread of kill points, not
					// an execution-long rain: a handful of process deaths at
					// seeded distances, and survival after the last one.
					killsLeft := 2 + rnd.Intn(4)
					store.killAt = 1 + rnd.Intn(4)

					res, fakes, lives := crashThenResume(t, pg.g, pg.script, "prop-"+name, store, func() {
						if killsLeft--; killsLeft > 0 {
							store.killAt = store.saves + 1 + rnd.Intn(3)
						} else {
							store.killAt = 0
						}
					})

					// P2: at most one settle ask per proposal and verb, summed
					// over every life the door saw.
					asked := map[string]int{}
					for _, f := range fakes {
						for _, req := range f.Settles() {
							asked[req.ProposalID+" "+req.Verb]++
						}
					}
					var keys []string
					for k, n := range asked {
						if n > 1 {
							t.Errorf("P2 broken: %s was asked %d times across %d lives", k, n, len(fakes))
						}
						keys = append(keys, k)
					}
					sort.Strings(keys)
					// A schedule that converged on the honesty window asked
					// the door for nothing; a converged run asked exactly
					// what the baseline asked.
					if res.Status == RunSucceeded {
						if strings.Join(keys, ",") != strings.Join(baseAsks, ",") {
							t.Errorf("settle asks = %v, want exactly the baseline's %v", keys, baseAsks)
						}
					}

					// P3: attempt ordinals are unique per node — a number
					// that appears twice is one answer folded twice.
					if dup := firstDuplicateAttempt(res); dup != "" {
						t.Errorf("P3 broken: %s", dup)
					}

					// P1/P4: the convergence split.
					have := project(t, res)
					if have.status == string(RunSucceeded) {
						if have.status != want.status {
							t.Errorf("status = %q, want %q", have.status, want.status)
						}
						for path, wn := range want.nodes {
							gn, ok := have.nodes[path]
							if !ok {
								t.Fatalf("node %s missing from the converged run", path)
							}
							if gn.state != wn.state || gn.result != wn.result || gn.disposed != wn.disposed {
								t.Errorf("node %s = %s (%q, disposed %q), want %s (%q, disposed %q)",
									path, gn.state, gn.result, gn.disposed, wn.state, wn.result, wn.disposed)
							}
						}
						t.Logf("seed %d: converged in %d lives, %d saves, asks %v", seed, lives, store.saves, keys)
						return
					}
					if !pg.honestFailure || !strings.Contains(res.Reason, ReasonSettlementInterrupted) {
						t.Errorf("run finished %s (%q) — a schedule without the write-ahead window must converge to the baseline",
							res.Status, res.Reason)
						return
					}
					// P4: the window was hit. The ask never reached the door,
					// the proposal stays undisposed, and the refusal is the
					// outcome — which is what the ledger's record on the run
					// says too.
					if len(asked) > 0 {
						t.Errorf("P4 broken: an interrupted settlement was still asked to the door %d time(s)", len(asked))
					}
					if land := nodeOf(t, res, "steps.land"); len(land.Settled) != 0 || len(land.Discards) != 0 {
						t.Errorf("P4 broken: the interrupted settlement recorded %d settled, %d discarded dispositions",
							len(land.Settled), len(land.Discards))
					}
					t.Logf("seed %d: honest failure in %d lives (%s)", seed, lives, res.Reason)
				})
			}
		})
	}
}

// crashThenResume drives one graph through a crash schedule until the run
// holds a final disposition, returning the result, every door the lives
// used, and the number of lives. nextKill is called after each simulated
// death to advance the schedule.
func crashThenResume(t *testing.T, g *ir.Graph, script map[string][]Completion, runID string, store *jsonStore, nextKill func()) (Result, []*FakeRuntime, int) {
	t.Helper()
	var fakes []*FakeRuntime
	lives := 0
	for {
		lives++
		if lives > 24 {
			t.Fatalf("the run did not converge after %d lives (last killAt=%d, saves=%d)",
				lives, store.killAt, store.saves)
		}
		fake := NewFakeRuntime(time.Unix(0, 0))
		fake.Script = script
		fakes = append(fakes, fake)
		cfg := Config{RunID: runID, Store: store}

		// A record whose disposition is final answers here: the last kill may
		// have taken the close with it, and the finished state IS the run's
		// answer — the record is read, not re-executed.
		if lives > 1 {
			cp, lerr := store.LoadCheckpoint(context.Background(), runID)
			if lerr != nil {
				t.Fatalf("load: %v", lerr)
			}
			if !cp.FinishedAt.IsZero() {
				return Result{Status: cp.Status, Reason: cp.Reason, Nodes: cp.Nodes}, fakes, lives
			}
		}

		var r *Run
		var err error
		if lives == 1 {
			r, err = New(g, fake, cfg)
		} else {
			cp, _ := store.LoadCheckpoint(context.Background(), runID)
			r, err = Resume(g, fake, cfg, cp)
		}
		if err == nil {
			_, err = r.Execute(context.Background())
		}
		if err == nil {
			return r.Result(), fakes, lives
		}
		// The kill may strike the resume's own closing checkpoint as much as
		// any mid-run save: both are process deaths, and the record they left
		// behind is what the next life loads.
		if !errors.Is(err, errProcessKilled) {
			t.Fatalf("life %d: %v", lives, err)
		}
		nextKill()
	}
}

// firstDuplicateAttempt reports the first attempt ordinal that appears twice
// in one node's history, or "".
func firstDuplicateAttempt(res Result) string {
	for _, n := range res.Nodes {
		seen := map[int]bool{}
		for _, a := range n.Attempts {
			if a == nil {
				continue
			}
			if seen[a.Number] {
				return fmt.Sprintf("node %s folded attempt %d twice", n.Path, a.Number)
			}
			seen[a.Number] = true
		}
	}
	return ""
}
