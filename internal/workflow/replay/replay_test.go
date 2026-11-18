package replay

// replay_test.go pins the door (yaah-0u0v.25): a recorded dispatch is handed
// back verbatim, an unrecorded one is refused loudly, settlements answer from
// the settle records without touching the world, and a call node's recorded
// outcome — bindings and child run id — is what the door answers with.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/workflow/exec"
	"github.com/buchenberg/yaah/internal/workflow/ir"
	"github.com/buchenberg/yaah/internal/workflow/trace"
)

func dispatchRecord(id, node string, attempt int, status, reason, result string) trace.Record {
	return trace.Record{
		Kind:       trace.KindDispatch,
		NodePath:   node,
		Attempt:    attempt,
		DispatchID: id,
		Status:     status,
		Reason:     reason,
		Result:     result,
	}
}

func recordedRun(t *testing.T, recs ...trace.Record) *Runtime {
	t.Helper()
	cp := exec.Checkpoint{RunID: "run-x", Status: exec.RunSucceeded, StartedAt: time.Unix(0, 0)}
	cp.Trace = append(cp.Trace, recs...)
	rt, err := New(cp)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return rt
}

// TestDoorReturnsTheRecordedEnvelope pins the substitution itself: the
// completion the door hands back is the one the trace recorded — status, stop
// reason, result, error body, recovery assert — with the key being the
// dispatch id, which is the node path (item key included) plus the attempt.
func TestDoorReturnsTheRecordedEnvelope(t *testing.T) {
	retryable := false
	rt := recordedRun(t,
		dispatchRecord("steps.work#1", "steps.work", 1, "error", "provider_error", ""),
		trace.Record{Kind: trace.KindDispatch, NodePath: "steps.retry#1", Attempt: 1,
			DispatchID: "steps.retry#1", Status: "error", Reason: "provider_error",
			Retryable: &retryable},
		trace.Record{Kind: trace.KindDispatch, NodePath: "steps.retry#2", Attempt: 2,
			DispatchID: "steps.retry#2", Status: "report", Result: "the answer"},
	)
	c, err := rt.Start(context.Background(), exec.Dispatch{
		Node:    &ir.Node{Path: "steps.retry"},
		Attempt: 2, DispatchID: "steps.retry#2",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, ok := rt.Completed(c)
	if !ok {
		t.Fatal("Started dispatch is not complete — a replay's answers are immediate")
	}
	if got.Status != "report" || got.Result != "the answer" {
		t.Errorf("completion = %+v, want the recorded report", got)
	}

	// The retried failure carries the recovery assert: the scheduler's retry
	// decision reads exactly this, so the replay retries where the original
	// did instead of failing the node.
	if _, err := rt.Start(context.Background(), exec.Dispatch{
		Node:    &ir.Node{Path: "steps.retry"},
		Attempt: 1, DispatchID: "steps.retry#1",
	}); err != nil {
		t.Fatalf("Start(attempt 1): %v", err)
	}
	first, ok := rt.Completed(exec.Handle{DispatchID: "steps.retry#1"})
	if !ok || first.Recovery == nil || first.Recovery.Retryable {
		t.Errorf("attempt 1 = %+v, want a recovery assert of retryable=false recorded", first)
	}
}

// TestDoorRefusesUnrecordedDispatches pins the loud failure: a dispatch the
// run never committed an answer for is refused, and the refusal names the
// dispatch and the run — nothing is regenerated.
func TestDoorRefusesUnrecordedDispatches(t *testing.T) {
	rt := recordedRun(t, dispatchRecord("steps.work#1", "steps.work", 1, "report", "completed", "done"))
	_, err := rt.Start(context.Background(), exec.Dispatch{
		Node:    &ir.Node{Path: "steps.other"},
		Attempt: 1, DispatchID: "steps.other#1",
	})
	if err == nil {
		t.Fatal("Start of an unrecorded dispatch succeeded; a replay must refuse to regenerate")
	}
	for _, want := range []string{"run-x", "steps.other#1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal = %q, want it to name %q", err.Error(), want)
		}
	}
	if _, err := rt.Start(context.Background(), exec.Dispatch{
		Node:    &ir.Node{Path: "steps.work"},
		Attempt: 1, DispatchID: "steps.work#1",
	}); err != nil {
		t.Fatalf("the recorded dispatch itself must start: %v", err)
	}
}

// TestDoorAnswersSettlementsFromTheRecords pins the settle half: the recorded
// outcome is the answer (the world is untouched by construction — the door
// holds no lane), and a disposition the run never recorded is a door error
// the scheduler folds as an outcome, never a regeneration.
func TestDoorAnswersSettlementsFromTheRecords(t *testing.T) {
	rt := recordedRun(t, dispatchRecord("steps.work#1", "steps.work", 1, "proposal", "completed", "the work"),
		trace.Record{Kind: trace.KindProposal, NodePath: "steps.work", Attempt: 1,
			DispatchID: "steps.work#1", ProposalID: "p-1"},
		trace.Record{Kind: trace.KindSettle, NodePath: "steps.land", ProposalID: "p-1",
			Action: "select", Status: "settled", Result: "work: selected"},
	)
	c, err := rt.Start(context.Background(), exec.Dispatch{
		Node:    &ir.Node{Path: "steps.work"},
		Attempt: 1, DispatchID: "steps.work#1",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	completion, _ := rt.Completed(c)
	if completion.ProposalID != "p-1" {
		t.Fatalf("completion proposal = %q, want the sealed handle from the companion record", completion.ProposalID)
	}

	s, err := rt.Settle(context.Background(), exec.SettleRequest{
		ProposalID: "p-1", Producer: "steps.work", Verb: "select",
	})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if s.Status != "settled" || s.LeftOpen || s.ProposalID != "p-1" || s.Action != "select" {
		t.Errorf("settled = %+v, want the recorded select of p-1", s)
	}

	_, err = rt.Settle(context.Background(), exec.SettleRequest{
		ProposalID: "p-9", Producer: "steps.work", Verb: "discard",
	})
	if err == nil {
		t.Fatal("Settle of an unrecorded proposal succeeded; a replay must refuse what it cannot name")
	}
	// The refusal names the proposal and the run: the fact to find is which
	// boundary never recorded the disposition the replay needs.
	for _, want := range []string{"run-x", "p-9", "discard"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal = %q, want it to name %q", err.Error(), want)
		}
	}
}

// TestDoorAnswersCallsFromTheRecordedNode pins the call half: a recorded
// successful call's bindings are the answer, the child run id is the recorded
// one, and a failed call recovers the child's own reason from the composed
// one so the node's fold states the same outcome it recorded.
func TestDoorAnswersCallsFromTheRecordedNode(t *testing.T) {
	cp := exec.Checkpoint{RunID: "run-x", Status: exec.RunSucceeded}
	cp.Trace = append(cp.Trace, dispatchRecord("steps.other#1", "steps.other", 1, "report", "completed", "x"))
	cp.Nodes = []*exec.NodeState{
		{Path: "steps.sub", Kind: ir.KindCall, State: exec.StateSucceeded,
			CallRunID: "run-x!steps.sub", Result: `{"summary":"done"}`},
		{Path: "steps.bad", Kind: ir.KindCall, State: exec.StateFailed,
			CallRunID: "run-x!steps.bad",
			Reason:    fmt.Sprintf("%s: subworkflow child (run run-x!steps.bad) — the child finished failed", exec.ReasonCallFailed)},
	}
	rt, err := New(cp)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := rt.Call(context.Background(), exec.CallRequest{
		Node: &ir.Node{Path: "steps.sub", Workflow: "child"}, Workflow: "child", NodePath: "steps.sub",
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !res.Succeeded || res.ChildRunID != "run-x!steps.sub" || res.Outputs["summary"] != "done" {
		t.Errorf("call result = %+v, want the recorded bindings under the recorded child id", res)
	}

	fail, err := rt.Call(context.Background(), exec.CallRequest{
		Node: &ir.Node{Path: "steps.bad", Workflow: "child"}, Workflow: "child", NodePath: "steps.bad",
	})
	if err != nil {
		t.Fatalf("Call (recorded failure): %v", err)
	}
	if fail.Succeeded || fail.ChildRunID != "run-x!steps.bad" || fail.Reason != "the child finished failed" {
		t.Errorf("failed call result = %+v, want the recorded child failure recovered", fail)
	}
}

// TestNewRefusesMalformedRecords pins the builder's own honesty: a trace that
// records one dispatch twice is refused — the door's answers are keyed, and a
// key that maps to two answers is a corruption to report, not a choice to
// make.
func TestNewRefusesMalformedRecords(t *testing.T) {
	cp := exec.Checkpoint{RunID: "run-x"}
	cp.Trace = []trace.Record{
		dispatchRecord("steps.work#1", "steps.work", 1, "report", "completed", "a"),
		dispatchRecord("steps.work#1", "steps.work", 1, "report", "completed", "b"),
	}
	_, err := New(cp)
	if err == nil {
		t.Fatal("New accepted a trace that records one dispatch twice")
	}
	for _, want := range []string{"run-x", "steps.work#1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal = %q, want it to name %q (the run and the dispatch)", err.Error(), want)
		}
	}
}

// TestDoorClockAndTelemetryAreInert pins the door's non-answer surface: the
// frozen clock, the immediate Block, the no-op cancellations and backoffs —
// none of which may invent elapsed time or door work.
func TestDoorClockAndTelemetryAreInert(t *testing.T) {
	rt := recordedRun(t, dispatchRecord("steps.work#1", "steps.work", 1, "report", "completed", "done"))
	if !rt.Now().Equal(time.Unix(0, 0)) {
		t.Errorf("Now = %v, want the recorded start frozen at the epoch", rt.Now())
	}
	if err := rt.Block(context.Background()); err != nil {
		t.Errorf("Block: %v", err)
	}
	if err := rt.Cancel(exec.Handle{DispatchID: "nowhere"}); err != nil {
		t.Errorf("Cancel: %v", err)
	}
	if err := rt.Backoff(context.Background(), time.Hour); err != nil {
		t.Errorf("Backoff: %v — a replay never sleeps through recorded time", err)
	}
	if ids := rt.DispatchIDs(); len(ids) != 1 || ids[0] != "steps.work#1" {
		t.Errorf("DispatchIDs = %v, want the recorded one", ids)
	}
}
