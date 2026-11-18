package exec

// abort_test.go pins the abort-time dispositions (yaah-0u0v.24): halt leaves
// every open proposal open and the close reports them, release_all and
// discard_all dispose of each open proposal through the same settlement door
// the settle nodes use, an abort during a fan-out cancels what is in flight
// before the pass runs, and a crash inside the pass never turns into a
// second ask on resume. All of it runs against the FakeRuntime — the real
// door is internal/workflow/host's business.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/workflow/trace"
)

// abortChain builds a two-step document: a proposal-lane producer that seals
// p-work, and a checker whose terminal failure aborts the run under the
// folded on_failure default — with the producer's proposal still open. The
// optional on_abort is the declaration under test.
func abortChain(name, onAbort string) string {
	y := strings.Replace(specHead, "%s", name, 1) + `  steps:
    - id: work
      kind: delegate
      role: developer
      lane: proposal
      prompt: do the work
    - id: check
      kind: delegate
      role: tester
      needs: [work]
      prompt: check it
`
	if onAbort != "" {
		y = strings.Replace(y, "  steps:", "  on_abort: "+onAbort+"\n  steps:", 1)
	}
	return y
}

// abortFailScript completes the producer with a sealed proposal and fails the
// checker terminally: the run aborts with the proposal it must not abandon.
func abortFailScript() map[string][]Completion {
	return map[string][]Completion{
		"steps.work":  {Proposal("p-work", 0)},
		"steps.check": {TerminalFailure("boom", "bad input", 0)},
	}
}

// settleRecordsOf collects the trace's settle records about one proposal.
func settleRecordsOf(res Result, proposalID string) []trace.Record {
	var out []trace.Record
	for _, rec := range res.Records {
		if rec.Kind == trace.KindSettle && rec.ProposalID == proposalID {
			out = append(out, rec)
		}
	}
	return out
}

// TestAbortHaltLeavesOpenAndReports pins halt: nothing is asked of the door,
// the proposals stay open for the operator or the prune sweep, and the
// close's reason names every one of them. An undeclared on_abort is halt —
// the loud default the author did not override.
func TestAbortHaltLeavesOpenAndReports(t *testing.T) {
	for _, onAbort := range []string{"", "halt"} {
		name := "undeclared"
		if onAbort != "" {
			name = onAbort
		}
		t.Run(name, func(t *testing.T) {
			g := graphFromYAML(t, abortChain("abort-halt-"+name, onAbort))
			res, fake := runGraph(t, g, abortFailScript(), Config{})
			if res.Status != RunAborted {
				t.Fatalf("status = %s reason=%q, want aborted\nlog:\n%s", res.Status, res.Reason, logOf(res))
			}
			want := "on_abort: halt leaves 1 proposal(s) open (p-work (steps.work))"
			if !strings.Contains(res.Reason, want) {
				t.Errorf("reason = %q, want it to report the residue: %s", res.Reason, want)
			}
			if asks := fake.Settles(); len(asks) > 0 {
				t.Errorf("halt asked the door for %d disposition(s); it asks for nothing", len(asks))
			}
			if recs := settleRecordsOf(res, "p-work"); len(recs) > 0 {
				t.Errorf("halt recorded %d settle record(s); the proposal was left alone", len(recs))
			}
		})
	}
}

// TestAbortSweepsOpenProposals pins the two sweeping policies: release_all
// and discard_all dispose of every open proposal through the door, the run
// ends with nothing left open, and both the event log and the trace carry
// each disposition.
func TestAbortSweepsOpenProposals(t *testing.T) {
	for _, tc := range []struct {
		policy string
		verb   string
	}{
		{"release_all", "release"},
		{"discard_all", "discard"},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			g := graphFromYAML(t, abortChain("abort-"+tc.policy, tc.policy))
			res, fake := runGraph(t, g, abortFailScript(), Config{})
			if res.Status != RunAborted {
				t.Fatalf("status = %s reason=%q, want aborted\nlog:\n%s", res.Status, res.Reason, logOf(res))
			}
			if strings.Contains(res.Reason, " proposal(s) open") {
				t.Errorf("reason = %q, want zero proposals left open\nlog:\n%s", res.Reason, logOf(res))
			}
			asks := fake.Settles()
			if len(asks) != 1 || asks[0].ProposalID != "p-work" || asks[0].Verb != tc.verb {
				t.Fatalf("door asked %v, want one %s of p-work", asks, tc.verb)
			}
			if asks := asks[0].Producer; asks != "steps.work" {
				t.Errorf("disposition producer = %q, want steps.work", asks)
			}
			for _, want := range []string{
				"abort_asked proposal_id=p-work action=" + tc.verb,
				"abort_settled proposal_id=p-work action=" + tc.verb + " status=settled left_open=false",
			} {
				if !strings.Contains(eventLog(res), want) {
					t.Errorf("event log lacks %q\nlog:\n%s", want, eventLog(res))
				}
			}
			// The trace carries both ends of each disposition: the ask and
			// the door's answer, marked as the run's own on_abort records.
			recs := settleRecordsOf(res, "p-work")
			if len(recs) != 2 {
				t.Fatalf("trace has %d settle record(s) for p-work, want ask and answer\nrecords:%+v", len(recs), recs)
			}
			if recs[0].Status != trace.StatusAsked || recs[0].Action != tc.verb || recs[0].Reason != "on_abort" {
				t.Errorf("ask record = %+v, want status=asked action=%s reason=on_abort", recs[0], tc.verb)
			}
			if recs[1].Status != SettlementStatusSettled || recs[1].Action != tc.verb || recs[1].Reason != "on_abort" {
				t.Errorf("settled record = %+v, want status=settled action=%s reason=on_abort", recs[1], tc.verb)
			}
		})
	}
}

// TestAbortSweepDoorErrorKeepsProposalOpen pins what a broken door leaves:
// the disposition is recorded as an error, the proposal keeps whatever state
// it had, and the close names it as open instead of pretending the policy
// was honored.
func TestAbortSweepDoorErrorKeepsProposalOpen(t *testing.T) {
	g := graphFromYAML(t, abortChain("abort-door-error", "release_all"))
	res, _ := runGraphScripted(t, g, abortFailScript(), Config{},
		nil, map[string]error{"p-work": errors.New("door offline")})
	if res.Status != RunAborted {
		t.Fatalf("status = %s reason=%q, want aborted\nlog:\n%s", res.Status, res.Reason, logOf(res))
	}
	want := "on_abort: release_all leaves 1 proposal(s) open (p-work (steps.work))"
	if !strings.Contains(res.Reason, want) {
		t.Errorf("reason = %q, want it to report what the broken door left open: %s", res.Reason, want)
	}
	if !strings.Contains(eventLog(res), "abort_settled proposal_id=p-work action=release status=error left_open=true door_error=door offline") {
		t.Errorf("event log lacks the error disposition\nlog:\n%s", eventLog(res))
	}
	recs := settleRecordsOf(res, "p-work")
	if len(recs) != 2 {
		t.Fatalf("trace has %d settle record(s) for p-work, want ask and answer\nrecords:%+v", len(recs), recs)
	}
	if recs[1].Status != SettlementStatusError || !strings.Contains(recs[1].Reason, "door offline") {
		t.Errorf("error record = %+v, want status=error with the door error as the reason", recs[1])
	}
}

// TestAbortDuringFanoutCancelsInFlightAndDisposes pins the order stop
// guarantees: the item still in flight is cancelled through the door BEFORE
// the dispositions are asked, and the proposal a completed item sealed is
// disposed of by the declared policy. The cancelled item sealed nothing, so
// the run ends with no proposal left open.
func TestAbortDuringFanoutCancelsInFlightAndDisposes(t *testing.T) {
	y := strings.Replace(specHead, "%s", "abort-fanout", 1) + `  on_abort: release_all
  steps:
    - id: f
      kind: fanout
      over: [a, b, c]
      as: ws
      max_parallel: 2
      on_item_failure: abort
      body:
        kind: delegate
        lane: proposal
        role: developer
        prompt: "work {{ ws }}"
      collect: results
`
	g := graphFromYAML(t, y)
	// The failure (items[1]) is dispatched before the held item (items[2]),
	// so it is the head of the commit order: its fold aborts the run while
	// the held item is still in flight, which is what stop's cancel loop
	// owes the door a Cancel for.
	script := map[string][]Completion{
		itemPathOf("steps.f", 0, 0): {Proposal("p-0", 0)},
		itemPathOf("steps.f", 1, 0): {TerminalFailure("boom", "bad input", 0)},
	}
	res, fake := runJoin(t, g, script, []string{itemPathOf("steps.f", 2, 0)}, Config{})
	if res.Status != RunAborted {
		t.Fatalf("status = %s reason=%q, want aborted\nlog:\n%s", res.Status, res.Reason, logOf(res))
	}
	cancels := fake.Cancels()
	if len(cancels) == 0 {
		t.Fatal("no attempt was cancelled — the in-flight item is owed its cancel before any disposition")
	}
	if !strings.HasPrefix(cancels[0], itemPathOf("steps.f", 2, 0)+"#") {
		t.Errorf("cancelled %v, want the in-flight item", cancels)
	}
	asks := fake.Settles()
	if len(asks) != 1 || asks[0].ProposalID != "p-0" || asks[0].Verb != "release" {
		t.Fatalf("door asked %v, want one release of p-0 (the cancelled item sealed nothing)", asks)
	}
	if strings.Contains(res.Reason, " proposal(s) open") {
		t.Errorf("reason = %q, want zero proposals left open\nlog:\n%s", res.Reason, logOf(res))
	}
}

// TestResumeAbortedSweepAsksEachProposalOnce pins the resume half of the
// discipline: the abort pass's ask is persisted before the door is called, so
// a crash between the two leaves "asked, outcome unknown" on the record, and
// the resumed run asks the proposals that were never asked and never asks one
// twice — whatever side of a crash it finds itself on. A proposal the first
// run disposed of stays disposed of: the ledger remembers the disposition.
func TestResumeAbortedSweepAsksEachProposalOnce(t *testing.T) {
	g := graphFromYAML(t, abortChain("resume-abort", "release_all"))
	script := abortFailScript()
	const runID = "run-abort"
	saves := checkpointBoundaries(t, g, script, runID, nil)

	var sawNeverAsked, sawUnknownOutcome, sawAlreadySwept bool
	for k := 1; k <= saves; k++ {
		t.Run(fmt.Sprintf("kill-at-%d", k), func(t *testing.T) {
			store := &jsonStore{killAt: k}
			_, fake1, err := runToKill(t, g, script, Config{RunID: runID, Store: store}, store, nil)
			if err != nil && !errors.Is(err, errProcessKilled) {
				t.Fatalf("killed run returned %v, want the kill", err)
			}
			cp, lerr := store.last(runID)
			if errors.Is(lerr, ErrRunNotFound) {
				return // killed before anything was written
			}
			if lerr != nil {
				t.Fatalf("last checkpoint: %v", lerr)
			}
			if !cp.FinishedAt.IsZero() {
				return // finished before the kill: the close is final
			}
			if cp.Status == RunRunning {
				return // killed before the abort: the resume rebuilds the run
			}
			// What the record says about the proposal before resuming: the
			// abort pass's asks live in the ledger, beside the open set.
			_, askedOnRecord := cp.Ledger.AbortAsked["p-work"]
			_, wasOpen := cp.Ledger.Open["p-work"]

			res, fake2, rerr := resumeOver(t, g, script, store, runID, "", nil)
			if rerr != nil {
				t.Fatalf("resume: %v", rerr)
			}
			if res.Status != RunAborted && !cp.FinishedAt.IsZero() {
				t.Fatalf("status = %s, want the aborted close to stay aborted", res.Status)
			}
			if strings.Contains(res.Reason, "steps.check: on_failure") == false {
				t.Fatalf("reason = %q, want the checker's abort to remain the run's reason", res.Reason)
			}
			// A proposal the first run disposed of stayed disposed of: no
			// second ask, and nothing re-opened.
			total := askCount(fake1, "p-work") + askCount(fake2, "p-work")
			if total > 1 {
				t.Fatalf("the door was asked to release p-work %d time(s); a proposal has at most one disposition", total)
			}
			switch {
			case askedOnRecord && askCount(fake1, "p-work") == 0:
				// Killed between the write-ahead ask and the call: the
				// outcome is unknown, so the resumed run refuses to ask
				// and reports the proposal as open.
				sawUnknownOutcome = true
				if total != 0 {
					t.Fatalf("door asked %d time(s) with an unanswered ask on the record, want 0 (never re-ask)", total)
				}
				want := "on_abort: release_all leaves 1 proposal(s) open (p-work (steps.work))"
				if !strings.Contains(res.Reason, want) {
					t.Errorf("reason = %q, want the unknown outcome reported as open: %s", res.Reason, want)
				}
			case askedOnRecord && askCount(fake1, "p-work") == 1:
				// Killed between the call and the answer's fold (or after
				// it): the ask reached the door, so it is not asked again.
				sawAlreadySwept = true
				if total != 1 {
					t.Fatalf("door asked %d time(s) for a proposal already asked for, want 1", total)
				}
			case !askedOnRecord && !wasOpen:
				// Killed before the producer's proposal was even sealed:
				// nothing to dispose of then, and nothing later re-opened.
				sawNeverAsked = true
			default:
				// Never asked, still open: the resumed run asks once —
				// the one ask the policy owes.
				if total != 1 {
					t.Fatalf("door asked %d time(s), want exactly the one deferred ask", total)
				}
			}
			if strings.Contains(res.Reason, " proposal(s) open") != strings.Contains(res.Reason, "leaves") {
				t.Errorf("reason = %q, want residue reported through the on_abort clause only", res.Reason)
			}
		})
	}
	if !sawUnknownOutcome || !sawAlreadySwept {
		t.Errorf("kill points covered: unknownOutcome=%t alreadySwept=%t neverSealed=%t; want the sweep's crash window reached",
			sawUnknownOutcome, sawAlreadySwept, sawNeverAsked)
	}
}

// askCount counts the door settle requests for one proposal id.
func askCount(fake *FakeRuntime, proposalID string) int {
	n := 0
	for _, req := range fake.Settles() {
		if req.ProposalID == proposalID {
			n++
		}
	}
	return n
}
