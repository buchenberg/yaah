// Package replay is the `--replay` door (yaah-0u0v.25): a workflow run's
// recorded trace, replayed through the same scheduler with zero model calls.
//
// The door is an exec.Runtime built from a COMPLETED run's checkpoint. It
// answers Start with the envelope the run recorded for exactly that
// DispatchID — which is "node path # attempt", the key the bead's design
// names as node id + item key + attempt — and Settle with the outcome the
// settle records hold. A record that is not there is refused loudly: a replay
// is a claim that this run's answers are already known, and an answer that is
// missing is a bug to find, not a hole to fill with a model call.
//
// What a replay reproduces — node outcomes, output bindings, dispositions —
// is everything the reducer derives deterministically from the substituted
// answers. What it does not reproduce is the world: settle answers are
// replayed from the records, not re-merged, so no worktree is touched; the
// wall-clock budget axis is inert because a replay never runs the wall clock;
// and door-side warnings (a declined artifact write, a contract verdict, an
// envelope's duration and model attribution) belong to the door that
// produced them, not to this one. The dispatch-count budget DOES replay: the
// scheduler counts the same starts, and it stops before the dispatch the
// original never made.
package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/workflow/exec"
	"github.com/buchenberg/yaah/internal/workflow/ir"
	"github.com/buchenberg/yaah/internal/workflow/trace"
)

var _ exec.Runtime = (*Runtime)(nil)

// Runtime is the replay door. Its one job is the lookup: everything the
// scheduler asks has an answer on the record, and the record is the only
// source — the door holds no runner and no model path, which is what makes
// "zero model calls" a property of the type rather than a promise of the
// configuration.
type Runtime struct {
	completions map[string]exec.Completion // dispatch id -> recorded envelope
	doorErrors  map[string]string          // dispatch id -> recorded door malfunction
	started     map[string]exec.Completion // dispatch id -> handed out at Start
	settles     map[string]exec.Settled    // "<proposal id> <verb>" -> recorded outcome
	calls       map[string]*exec.NodeState // call node path -> recorded state
	runID       string                     // the original run, for refusal prose
	clock       time.Time                  // the recorded start: the replay's frozen clock
}

// New builds the door from a completed run's checkpoint. The trace records
// are the dispatch source (the design's rule: the same records the executor
// wrote, no replay format of its own); the node states carry what the trace
// deliberately does not duplicate — a call node's recorded bindings and the
// child run id — which is the record the reducer itself persisted.
func New(cp exec.Checkpoint) (*Runtime, error) {
	if cp.RunID == "" {
		return nil, fmt.Errorf("workflow/replay: checkpoint with no run id")
	}
	rt := &Runtime{
		completions: map[string]exec.Completion{},
		doorErrors:  map[string]string{},
		started:     map[string]exec.Completion{},
		settles:     map[string]exec.Settled{},
		calls:       map[string]*exec.NodeState{},
		runID:       cp.RunID,
		clock:       cp.StartedAt,
	}
	if rt.clock.IsZero() {
		rt.clock = time.Unix(0, 0)
	}
	for i := range cp.Trace {
		rec := cp.Trace[i]
		switch rec.Kind {
		case trace.KindDispatch:
			if rec.DispatchID == "" {
				return nil, fmt.Errorf("workflow/replay: run %s holds a dispatch record %d with no dispatch id", cp.RunID, rec.Seq)
			}
			switch rec.Status {
			case trace.StatusStarted:
				// The start marker a dispatch record carries: it says the
				// attempt was handed to the door, not what came back. The
				// answer is the record that follows it.
				continue
			case trace.StatusRuntimeError:
				// The door itself malfunctioned on this attempt: the replay
				// refuses the same way, with the recorded reason — a door
				// that could not start work the first time is not given a
				// second chance to invent an answer.
				if _, dup := rt.doorErrors[rec.DispatchID]; dup {
					return nil, fmt.Errorf("workflow/replay: run %s records dispatch %s twice", cp.RunID, rec.DispatchID)
				}
				rt.doorErrors[rec.DispatchID] = rec.Reason
			default:
				if _, dup := rt.completions[rec.DispatchID]; dup {
					return nil, fmt.Errorf("workflow/replay: run %s records dispatch %s twice", cp.RunID, rec.DispatchID)
				}
				rt.completions[rec.DispatchID] = completionOf(rec)
			}
		case trace.KindProposal:
			// The proposal record is the companion of the dispatch record with
			// the same id: it carries the proposal the attempt sealed, which
			// the envelope's Completion mirrors as ProposalID.
			if rec.DispatchID == "" || rec.ProposalID == "" {
				continue
			}
			c := rt.completions[rec.DispatchID]
			c.ProposalID = rec.ProposalID
			rt.completions[rec.DispatchID] = c
		case trace.KindSettle:
			// Only the outcome records: an "asked" record states intent, and
			// the answer that follows it is the fact a replay names.
			if rec.ProposalID == "" || rec.Action == "" || rec.Status == trace.StatusAsked {
				continue
			}
			key := rec.ProposalID + " " + rec.Action
			if _, dup := rt.settles[key]; dup {
				return nil, fmt.Errorf("workflow/replay: run %s records the disposition of %s (%s) twice", cp.RunID, rec.ProposalID, rec.Action)
			}
			rt.settles[key] = exec.Settled{
				ProposalID: rec.ProposalID,
				Action:     rec.Action,
				Status:     rec.Status,
				Outcome:    rec.Result,
				Files:      rec.Files,
				Error:      settleErrorOf(rec),
				// A settled disposition is the one that closed the proposal;
				// every other outcome the door reports leaves it open, which
				// is the invariant the fold bookkeeping reads.
				LeftOpen: rec.Status != "settled",
			}
		}
	}
	for _, n := range cp.Nodes {
		if n != nil && n.Kind == ir.KindCall && n.State.Terminal() {
			rt.calls[n.Path] = n
		}
	}
	return rt, nil
}

// completionOf rebuilds the envelope facts the dispatch record carries: the
// status verbatim, the typed stop reason in Reason (with the door's own
// malfunction text first when that is what the run recorded), the result body
// the fold reads output bindings from, the error body a failure reason
// quotes, and the recovery assert a retry decision reads — all fields the
// trace gained so a replay re-derives the run's own sequence (yaah-0u0v.25).
func completionOf(rec trace.Record) exec.Completion {
	c := exec.Completion{
		Status:     rec.Status,
		StopReason: rec.Reason,
		Result:     rec.Result,
		Error:      rec.Error,
		Lane:       rec.Lane,
	}
	if rec.Retryable != nil {
		c.Recovery = &exec.Recovery{Retryable: *rec.Retryable}
	}
	return c
}

// settleErrorOf states the error a non-settled disposition reported. The
// settle records keep the specific cause in Reason — a door malfunction, a
// refused ask, or the error a completed disposition reported.
func settleErrorOf(rec trace.Record) string {
	if rec.Status == "settled" {
		return ""
	}
	return rec.Reason
}

// Start answers with the recorded envelope for one dispatch. There is no
// attempt to launch: the completion is already known, so it is handed to
// Completed the moment the scheduler asks for it — and a dispatch the run
// never answered is refused, because the one thing a replay must not do is
// regenerate what it claims to be replaying.
func (rt *Runtime) Start(_ context.Context, d exec.Dispatch) (exec.Handle, error) {
	if d.Node == nil {
		return exec.Handle{}, fmt.Errorf("workflow/replay: dispatch with no node")
	}
	if d.DispatchID == "" {
		return exec.Handle{}, fmt.Errorf("workflow/replay: dispatch with no dispatch id")
	}
	c, ok := rt.completions[d.DispatchID]
	if !ok {
		// A recorded door malfunction is replayed as one: the scheduler folds
		// the same runtime-error outcome, with the reason the run recorded.
		if why, broke := rt.doorErrors[d.DispatchID]; broke {
			return exec.Handle{}, fmt.Errorf("%s", why)
		}
		return exec.Handle{}, fmt.Errorf(
			"workflow/replay: run %s recorded no output for dispatch %s — the run that would answer it never committed one; refusing to regenerate",
			rt.runID, d.DispatchID)
	}
	rt.started[d.DispatchID] = c
	return exec.Handle{DispatchID: d.DispatchID}, nil
}

// Completed reports the recorded envelope. The answer is stable because it
// was already stable: the record does not change.
func (rt *Runtime) Completed(h exec.Handle) (exec.Completion, bool) {
	c, ok := rt.started[h.DispatchID]
	return c, ok
}

// Block returns immediately: every started attempt is completable the moment
// it starts, so there is nothing to wait for and no door that could stall.
func (rt *Runtime) Block(context.Context) error { return nil }

// Cancel is a no-op that cannot fail: nothing real is ever in flight behind
// this door, so the scheduler's cancel loop receives the answer it wants —
// "asked and acknowledged".
func (rt *Runtime) Cancel(exec.Handle) error { return nil }

// Backoff does not wait: a replay re-folds a retry the original already
// slept for, and sleeping through recorded time is the one way a replay
// could be slower than the original.
func (rt *Runtime) Backoff(context.Context, time.Duration) error { return nil }

// Now is the replay's frozen clock: the recorded start instant, held still.
// It keeps wall-clock budget behavior deterministic (no replay can breach a
// wall clock it never ran) at the cost of the axis being inert — which the
// door documents, because inventing elapsed time would be inventing facts.
func (rt *Runtime) Now() time.Time { return rt.clock }

// Settle answers with the recorded disposition for one proposal under one
// verb. The world is not touched: an apply is not re-merged, a release does
// not delete the worktree that was already gone. A disposition the run never
// recorded is a door error, which the scheduler folds as an error outcome
// that leaves the proposal open — the loud failure, never regeneration.
func (rt *Runtime) Settle(_ context.Context, req exec.SettleRequest) (exec.Settled, error) {
	if req.ProposalID == "" {
		return exec.Settled{}, fmt.Errorf("workflow/replay: settle with no proposal id")
	}
	key := req.ProposalID + " " + req.Verb
	s, ok := rt.settles[key]
	if !ok {
		return exec.Settled{
			ProposalID: req.ProposalID,
			Producer:   req.Producer,
			Action:     req.Verb,
			Status:     exec.SettlementStatusError,
			Error: fmt.Sprintf("replay: no recorded disposition of %s (%s) in run %s",
				req.ProposalID, req.Verb, rt.runID),
			LeftOpen: true,
		}, fmt.Errorf(
			"workflow/replay: run %s recorded no disposition of %s (%s) — refusing to settle what the record does not cover",
			rt.runID, req.ProposalID, req.Verb)
	}
	return s, nil
}

// Call answers with the call node's recorded outcome: the child's bindings the
// run already folded are the node's value, and the child's run id is the
// identity its own records live under. The child is NOT re-run — its answers
// were the point of recording them — and the id naming stays the derivation
// the composition uses, so a replay's node record points at the child run
// the original produced. Nothing is written there: the replay's caller
// composed no store, so the child's records are read-only facts.
func (rt *Runtime) Call(_ context.Context, req exec.CallRequest) (exec.CallResult, error) {
	st, ok := rt.calls[req.NodePath]
	if !ok {
		return exec.CallResult{}, fmt.Errorf(
			"workflow/replay: run %s recorded no call at %s — refusing to run a subworkflow the record does not cover",
			rt.runID, req.NodePath)
	}
	if st.State == exec.StateSucceeded {
		var outputs map[string]string
		if st.Result != "" {
			if err := json.Unmarshal([]byte(st.Result), &outputs); err != nil {
				return exec.CallResult{}, fmt.Errorf("workflow/replay: decoding recorded call bindings of %s: %w", req.NodePath, err)
			}
		}
		return exec.CallResult{
			Succeeded:  true,
			ChildRunID: st.CallRunID,
			Outputs:    outputs,
		}, nil
	}
	switch {
	case st.CallRunID == "":
		// The door itself failed before any child ran: the honest answer is a
		// door error, because it is at the door the original failed too.
		return exec.CallResult{}, fmt.Errorf(
			"workflow/replay: run %s records call %s as a door failure with no child run (original: %s)",
			rt.runID, req.NodePath, st.Reason)
	default:
		return exec.CallResult{
			Succeeded:  false,
			ChildRunID: st.CallRunID,
			Status:     exec.RunFailed,
			Reason:     callChildReason(req.Node.Workflow, st),
		}, nil
	}
}

// callChildReason recovers the child's own reason from the reason the parent
// composed for the call node: the fold's composition is fixed —
// "call_failed: subworkflow <name> (run <id>)[ — <child reason>]" — so the
// prefix names what to cut, and a reason that does not match the recorded id
// is carried verbatim rather than guessed at.
func callChildReason(workflow string, st *exec.NodeState) string {
	prefix := fmt.Sprintf("%s: subworkflow %s (run %s)",
		exec.ReasonCallFailed, workflow, st.CallRunID)
	if st.Reason == prefix || st.Reason == "" {
		return ""
	}
	if rest, ok := strings.CutPrefix(st.Reason, prefix); ok {
		return strings.TrimPrefix(rest, " — ")
	}
	return st.Reason
}

// DispatchIDs lists the recorded dispatch ids in canonical order, so a report
// can say what a replay has in hand without knowing the door's keying.
func (rt *Runtime) DispatchIDs() []string {
	out := make([]string, 0, len(rt.completions))
	for id := range rt.completions {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
