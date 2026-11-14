package exec

// settle_test.go pins the settlement core (yaah-0u0v.13): the four verbs
// reach the door once each, a proposal is settled exactly once, a run ends
// with zero open proposals unless the caller declared otherwise, and
// on_conflict: stop halts with a machine-readable conflict. All of it runs
// against the FakeRuntime — the door is internal/workflow/host's business.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// settleChain builds a two-step document: a proposal-lane delegate and a
// settle node that disposes of what it sealed, with the given action verb.
func settleChain(name, action string) string {
	return strings.Replace(specHead, "%s", name, 1) + `  steps:
    - id: work
      kind: delegate
      role: developer
      lane: proposal
      prompt: do the work
    - id: land
      kind: settle
      needs: [work]
      proposals: { from: steps.work.output.proposal_id }
      action: ` + action + `
`
}

// runGraphScripted is runGraph with the fake's settle scripting attached:
// settleScript overrides the settlement reported per proposal id, settleErrs
// scripts a door malfunction per proposal id.
func runGraphScripted(t *testing.T, g *ir.Graph, script map[string][]Completion, cfg Config,
	settleScript map[string]Settled, settleErrs map[string]error) (Result, *FakeRuntime) {
	t.Helper()
	rt := NewFakeRuntime(time.Unix(0, 0))
	rt.Script = script
	rt.SettleScript = settleScript
	rt.SettleErr = settleErrs
	r, err := New(g, rt, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := r.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v (events so far:\n%s)", err, strings.Join(r.Events(), "\n"))
	}
	return res, rt
}

func logOf(res Result) string { return strings.Join(res.Events, "\n") }

func TestSettleSelectHappyChain(t *testing.T) {
	g := graphFromYAML(t, settleChain("settle-chain", "select"))
	res, fake := runGraph(t, g, map[string][]Completion{
		"steps.work": {Proposal("prop-1", 0)},
	}, Config{})

	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q, want succeeded\nlog:\n%s", res.Status, res.Reason, logOf(res))
	}
	land := nodeOf(t, res, "steps.land")
	if land.State != StateSucceeded || len(land.Settled) != 1 {
		t.Fatalf("land = %+v, want one settlement\nlog:\n%s", land, logOf(res))
	}
	s := land.Settled[0]
	if s.ProposalID != "prop-1" || s.Action != "select" || s.Status != "settled" || s.LeftOpen {
		t.Errorf("settled = %+v, want prop-1 select settled, nothing open", s)
	}
	reqs := fake.Settles()
	if len(reqs) != 1 || reqs[0].ProposalID != "prop-1" || reqs[0].Verb != "select" {
		t.Errorf("door calls = %+v, want exactly one settle of prop-1 with select", reqs)
	}
}

// TestSettleFourVerbs exercises the whole settlement vocabulary — the bead's
// acceptance criterion — and pins that an empty policy reaches the door empty:
// the path_disjoint default is the door's constant, not the scheduler's.
func TestSettleFourVerbs(t *testing.T) {
	for _, verb := range []string{"select", "apply", "release", "discard"} {
		t.Run(verb, func(t *testing.T) {
			g := graphFromYAML(t, settleChain("verb-"+verb, verb))
			res, fake := runGraph(t, g, map[string][]Completion{
				"steps.work": {Proposal("prop-"+verb, 0)},
			}, Config{})
			if res.Status != RunSucceeded {
				t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, logOf(res))
			}
			reqs := fake.Settles()
			if len(reqs) != 1 || reqs[0].Verb != verb {
				t.Fatalf("door calls = %+v, want one %s", reqs, verb)
			}
			if reqs[0].Policy != "" {
				t.Errorf("policy = %q, want empty — the default belongs to the door", reqs[0].Policy)
			}
		})
	}
}

// TestDoubleSettleRefused pins WF021's "settled exactly once": a second node
// pointing at an already-settled proposal fails instead of re-settling.
func TestDoubleSettleRefused(t *testing.T) {
	y := strings.Replace(specHead, "%s", "double-settle", 1) + `  steps:
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
    - id: land_again
      kind: settle
      needs: [land]
      proposals: { from: steps.work.output.proposal_id }
      action: discard
`
	g := graphFromYAML(t, y)
	res, fake := runGraph(t, g, map[string][]Completion{
		"steps.work": {Proposal("prop-once", 0)},
	}, Config{})

	if res.Status != RunAborted {
		t.Fatalf("status = %s, want aborted\nlog:\n%s", res.Status, logOf(res))
	}
	again := nodeOf(t, res, "steps.land_again")
	if again.State != StateFailed || !strings.Contains(again.Reason, ReasonProposalAlreadySettled) {
		t.Errorf("land_again = %s reason=%q, want an already-settled refusal\nlog:\n%s",
			again.State, again.Reason, logOf(res))
	}
	if n := len(fake.Settles()); n != 1 {
		t.Errorf("door calls = %d, want 1 — the second settle must never reach the lane", n)
	}
}

// TestOpenProposalAtRunEnd pins the zero-open-proposals criterion: a sealed
// proposal nobody disposed of fails the run, and only an explicit caller
// declaration (OpenProposals) lets the run finish succeeded with it open.
func TestOpenProposalAtRunEnd(t *testing.T) {
	y := strings.Replace(specHead, "%s", "open-at-end", 1) + `  steps:
    - id: work
      kind: delegate
      role: developer
      lane: proposal
      prompt: do the work
`
	build := func() (*ir.Graph, map[string][]Completion) {
		return graphFromYAML(t, y), map[string][]Completion{
			"steps.work": {Proposal("prop-leak", 0)},
		}
	}

	t.Run("strict", func(t *testing.T) {
		g, script := build()
		res, _ := runGraph(t, g, script, Config{})
		if res.Status != RunFailed {
			t.Fatalf("status = %s, want failed\nlog:\n%s", res.Status, logOf(res))
		}
		if !strings.Contains(res.Reason, "prop-leak") || !strings.Contains(res.Reason, ReasonOpenProposalsAtExit) {
			t.Errorf("reason = %q, want it to name the open proposal", res.Reason)
		}
	})

	t.Run("declared_open", func(t *testing.T) {
		g, script := build()
		res, _ := runGraph(t, g, script, Config{OpenProposals: true})
		if res.Status != RunSucceeded {
			t.Fatalf("status = %s reason=%q, want succeeded under an explicit declaration\nlog:\n%s",
				res.Status, res.Reason, logOf(res))
		}
	})
}

// TestUnsupportedSettleVerbRefused pins that continue/fork are not settlement
// verbs: the scheduler refuses them itself and never wakes the door. Spec
// parsing already rejects them, so the graph is mutated after resolution —
// this covers the defense-in-depth path against a programmatically built
// graph.
func TestUnsupportedSettleVerbRefused(t *testing.T) {
	for _, verb := range []string{"continue", "fork"} {
		t.Run(verb, func(t *testing.T) {
			g := graphFromYAML(t, settleChain("verb-refused", "select"))
			for _, n := range g.Steps {
				if n.Kind == ir.KindSettle {
					n.Action = ir.SettleAction(verb)
				}
			}
			res, fake := runGraph(t, g, map[string][]Completion{
				"steps.work": {Proposal("prop-x", 0)},
			}, Config{})
			if res.Status != RunAborted {
				t.Fatalf("status = %s, want aborted\nlog:\n%s", res.Status, logOf(res))
			}
			land := nodeOf(t, res, "steps.land")
			if !strings.Contains(land.Reason, ReasonSettleActionUnsupported) ||
				!strings.Contains(land.Reason, "select|apply|release|discard") {
				t.Errorf("reason = %q, want the vocabulary refusal", land.Reason)
			}
			if n := len(fake.Settles()); n != 0 {
				t.Errorf("door calls = %d, want 0 — a refused verb never reaches the lane", n)
			}
		})
	}
}

// TestOnConflictStopHalts pins the acceptance criterion: on_conflict: stop
// (the default) halts the run with a machine-readable conflict — the overlap
// paths named in the failure, not prose about a merge.
func TestOnConflictStopHalts(t *testing.T) {
	g := graphFromYAML(t, settleChain("conflict-stop", "apply"))
	res, _ := runGraphScripted(t, g, map[string][]Completion{
		"steps.work": {Proposal("prop-conflict", 0)},
	}, Config{}, map[string]Settled{
		"prop-conflict": SettleConflicted("prop-conflict", "apply", "a.go", "b.go"),
	}, nil)

	if res.Status != RunAborted {
		t.Fatalf("status = %s, want aborted\nlog:\n%s", res.Status, logOf(res))
	}
	land := nodeOf(t, res, "steps.land")
	for _, want := range []string{"a.go", "b.go", "conflict"} {
		if !strings.Contains(land.Reason, want) {
			t.Errorf("reason = %q, want it to name %q", land.Reason, want)
		}
	}
	if len(land.Settled) != 1 || land.Settled[0].Status != "conflict" || !land.Settled[0].LeftOpen {
		t.Errorf("settled = %+v, want one conflict record left open", land.Settled)
	}
}

// TestOnConflictSkipDownstreamCatch pins the WF024 catch semantics: skip
// closes the node but leaves the proposal open, and a downstream settle still
// disposes of it — the run ends with zero open proposals.
func TestOnConflictSkipDownstreamCatch(t *testing.T) {
	y := strings.Replace(specHead, "%s", "conflict-skip", 1) + `  steps:
    - id: work
      kind: delegate
      role: developer
      lane: proposal
      prompt: do the work
    - id: land
      kind: settle
      needs: [work]
      proposals: { from: steps.work.output.proposal_id }
      action: apply
      on_conflict: skip
    - id: catch
      kind: settle
      needs: [land]
      proposals: { from: steps.work.output.proposal_id }
      action: discard
`
	g := graphFromYAML(t, y)
	res, fake := runGraphScripted(t, g, map[string][]Completion{
		"steps.work": {Proposal("prop-skipped", 0)},
	}, Config{}, map[string]Settled{
		"prop-skipped apply": SettleConflicted("prop-skipped", "apply", "a.go"),
	}, nil)

	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q, want succeeded\nlog:\n%s", res.Status, res.Reason, logOf(res))
	}
	land := nodeOf(t, res, "steps.land")
	if land.State != StateSucceeded || len(land.Settled) != 1 || !land.Settled[0].LeftOpen {
		t.Fatalf("land = %+v, want succeeded with the conflict left open", land)
	}
	catch := nodeOf(t, res, "steps.catch")
	if catch.State != StateSucceeded || len(catch.Settled) != 1 || catch.Settled[0].Action != "discard" {
		t.Fatalf("catch = %+v, want the downstream discard to dispose of it", catch)
	}
	if n := len(fake.Settles()); n != 2 {
		t.Errorf("door calls = %d, want 2 (conflicted apply + catch discard)", n)
	}
}

// TestSettleDoorErrorFailsNode pins that a lane error (an unknown proposal
// id, a dead registry) surfaces as a node failure carrying the door's error,
// with the proposal counted as still open.
func TestSettleDoorErrorFailsNode(t *testing.T) {
	g := graphFromYAML(t, settleChain("door-error", "select"))
	res, _ := runGraphScripted(t, g, map[string][]Completion{
		"steps.work": {Proposal("prop-gone", 0)},
	}, Config{}, nil, map[string]error{
		"prop-gone": errors.New("unknown proposal"),
	})

	if res.Status != RunAborted {
		t.Fatalf("status = %s, want aborted\nlog:\n%s", res.Status, logOf(res))
	}
	land := nodeOf(t, res, "steps.land")
	if !strings.Contains(land.Reason, "unknown proposal") {
		t.Errorf("reason = %q, want the door's error verbatim", land.Reason)
	}
}
