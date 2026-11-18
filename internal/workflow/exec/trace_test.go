package exec

// trace_test.go pins the trace projection (yaah-0u0v.17): every lifecycle
// fact the reducer folds is also recorded as one typed trace record, and the
// record set carries what `explain --from-trace` reconstructs — no transcript.

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/workflow/trace"
)

func TestTraceRecordsEmittedForChain(t *testing.T) {
	g := graphFromYAML(t, strings.Replace(chainYAML, "%s", "trace-chain", 1))
	res, _ := runGraph(t, g, okScript("steps.a", "steps.b", "steps.c"), Config{})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, eventLog(res))
	}
	counts := map[trace.Kind]int{}
	for _, r := range res.Records {
		counts[r.Kind]++
	}
	want := map[trace.Kind]int{
		trace.KindRunStarted:   1,
		trace.KindRunEnded:     1,
		trace.KindNodeStarted:  3,
		trace.KindNodeFinished: 3,
		trace.KindDispatch:     3,
	}
	for k, n := range want {
		if counts[k] < n {
			t.Errorf("%s = %d, want at least %d", k, counts[k], n)
		}
	}
}

func TestTraceRecordsCarryDispatchAndOutcome(t *testing.T) {
	g := graphFromYAML(t, strings.Replace(chainYAML, "%s", "trace-dispatch", 1))
	res, _ := runGraph(t, g, okScript("steps.a", "steps.b", "steps.c"), Config{})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q", res.Status, res.Reason)
	}
	var sawDispatchA, sawFinishedA bool
	for _, r := range res.Records {
		if r.Kind == trace.KindDispatch && r.NodePath == "steps.a" {
			sawDispatchA = true
			if r.DispatchID == "" || r.Attempt < 1 {
				t.Errorf("dispatch record for steps.a lacks id/attempt: %+v", r)
			}
		}
		if r.Kind == trace.KindNodeFinished && r.NodePath == "steps.a" {
			sawFinishedA = true
			if r.Status != string(StateSucceeded) {
				t.Errorf("steps.a finished status = %q, want succeeded", r.Status)
			}
		}
	}
	if !sawDispatchA || !sawFinishedA {
		t.Errorf("missing steps.a records: dispatch=%v finished=%v", sawDispatchA, sawFinishedA)
	}
}

func TestTraceRecordsEmitProposalAndSettle(t *testing.T) {
	g := graphFromYAML(t, settleChain("trace-settle", "select"))
	script := map[string][]Completion{"steps.work": {Proposal("prop-1", 0)}}
	res, _ := runGraph(t, g, script, Config{})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q", res.Status, res.Reason)
	}
	counts := map[trace.Kind]int{}
	for _, r := range res.Records {
		counts[r.Kind]++
	}
	if counts[trace.KindProposal] < 1 {
		t.Errorf("proposal records = %d, want at least 1", counts[trace.KindProposal])
	}
	if counts[trace.KindSettle] < 1 {
		t.Errorf("settle records = %d, want at least 1", counts[trace.KindSettle])
	}
}

// TestTraceFinishedCallRecordCarriesChildRunID pins the .23 residue on the
// trace side: a call node's finished record must name the child run, so
// `explain --from-trace` can follow the call without the checkpoint.
func TestTraceFinishedCallRecordCarriesChildRunID(t *testing.T) {
	t.Run("succeeded", func(t *testing.T) {
		calls := map[string][]CallResult{
			"helper": {{
				Succeeded:  true,
				ChildRunID: "caller!steps.sub",
				Status:     RunSucceeded,
				Outputs:    map[string]string{"summary": "done"},
			}},
		}
		res, _ := runCallGraph(t, callYAML, Config{RunID: "caller"}, calls, nil)
		if res.Status != RunSucceeded {
			t.Fatalf("status = %s reason %q", res.Status, res.Reason)
		}
		assertChildRunIDOnFinished(t, res, "caller!steps.sub")
	})
	t.Run("failed", func(t *testing.T) {
		calls := map[string][]CallResult{
			"helper": {{
				Succeeded:  false,
				ChildRunID: "caller!steps.sub",
				Status:     RunFailed,
				Reason:     "boom",
			}},
		}
		res, _ := runCallGraph(t, callYAML, Config{RunID: "caller"}, calls, nil)
		if res.Status == RunSucceeded {
			t.Fatalf("status = %s, want a failed child to sink the run", res.Status)
		}
		assertChildRunIDOnFinished(t, res, "caller!steps.sub")
	})
}

func assertChildRunIDOnFinished(t *testing.T, res Result, want string) {
	t.Helper()
	saw := false
	for _, r := range res.Records {
		if r.Kind != trace.KindNodeFinished || r.NodePath != "steps.sub" {
			continue
		}
		saw = true
		if r.ChildRunID != want {
			t.Errorf("finished record ChildRunID = %q, want %q", r.ChildRunID, want)
		}
	}
	if !saw {
		t.Fatalf("no finished trace record for steps.sub:\n%s", strings.Join(res.Events, "\n"))
	}
}
