package exec

// persist_test.go pins durable run state (yaah-0u0v.16) against the reducer
// itself: a run killed at EVERY checkpoint boundary must resume and finish
// without dispatching a node the record already shows as done and without asking
// the door to settle a proposal twice.
//
// The store here is a JSON-round-tripping in-memory double. The round trip is not
// a convenience: it is the same serialization the SQLite adapter performs, so
// every test in this file also pins that a NodeState survives encoding — a fact
// that would otherwise only fail in production, on the one field somebody added.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// errProcessKilled is the simulated hard stop: the store refuses the write and
// nothing after it happens, which is exactly the window a killed process leaves
// behind.
var errProcessKilled = errors.New("simulated process kill")

// jsonStore is an in-memory exec.Store that serializes every checkpoint, so a
// stored state is never aliased by the live run that produced it.
type jsonStore struct {
	cp     Checkpoint
	events []string
	saves  int
	// killAt kills the process on the Nth save: that save and everything after it
	// never lands.
	killAt int
	killed bool
	loads  int
}

func (s *jsonStore) SaveCheckpoint(_ context.Context, cp Checkpoint) error {
	s.saves++
	stored, err := cloneCheckpoint(cp)
	if err != nil {
		return err
	}
	// The event log accumulates as the table does: rows already written stay,
	// and the new lines append by sequence number.
	events := append([]string(nil), s.events[:cp.EventBase]...)
	events = append(events, cp.Events...)
	s.events = events
	stored.Events = nil
	stored.EventBase = 0
	s.cp = stored
	if s.killAt > 0 && s.saves == s.killAt {
		// The checkpoint lands and THEN the process dies, so a resume sees
		// exactly this durable boundary — the one the test pins.
		s.killed = true
		return errProcessKilled
	}
	return nil
}

func (s *jsonStore) LoadCheckpoint(_ context.Context, runID string) (Checkpoint, error) {
	s.loads++
	if s.cp.RunID == "" || s.cp.RunID != runID {
		return Checkpoint{}, fmt.Errorf("%w: %s", ErrRunNotFound, runID)
	}
	out, err := cloneCheckpoint(s.cp)
	if err != nil {
		return Checkpoint{}, err
	}
	out.Events = append([]string(nil), s.events...)
	out.EventBase = len(out.Events)
	return out, nil
}

// last returns the durable state, or reports that nothing was ever written.
func (s *jsonStore) last(runID string) (Checkpoint, error) {
	return s.LoadCheckpoint(context.Background(), runID)
}

// Close is a no-op: the jsonStore owns no OS resource.
func (s *jsonStore) Close() error { return nil }

func cloneCheckpoint(cp Checkpoint) (Checkpoint, error) {
	b, err := json.Marshal(cp)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("encoding checkpoint: %w", err)
	}
	var out Checkpoint
	if err := json.Unmarshal(b, &out); err != nil {
		return Checkpoint{}, fmt.Errorf("decoding checkpoint: %w", err)
	}
	return out, nil
}

// runToKill drives a fresh run over the store until the simulated kill fires (or
// the run finishes), returning the run and the store.
func runToKill(t *testing.T, g *ir.Graph, script map[string][]Completion, cfg Config, store *jsonStore, settleScript map[string]Settled) (*Run, *FakeRuntime, error) {
	t.Helper()
	rt := NewFakeRuntime(time.Unix(0, 0))
	rt.Script = script
	rt.SettleScript = settleScript
	r, err := New(g, rt, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, xerr := r.Execute(context.Background())
	return r, rt, xerr
}

// resumeOver reloads the durable state and drives the resumed run to its
// disposition, returning the result and the door.
func resumeOver(t *testing.T, g *ir.Graph, script map[string][]Completion, store *jsonStore, runID, specHash string, settleScript map[string]Settled) (Result, *FakeRuntime, error) {
	t.Helper()
	cp, err := store.LoadCheckpoint(context.Background(), runID)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	rt := NewFakeRuntime(time.Unix(0, 0))
	rt.Script = script
	rt.SettleScript = settleScript
	// The run id is deliberately NOT passed through: Resume takes it from the
	// record, and a resume that renamed the run would be a new run.
	r, err := Resume(g, rt, Config{RunID: "ignored-by-resume", Store: store, SpecHash: specHash}, cp)
	if err != nil {
		return Result{}, rt, err
	}
	res, err := r.Execute(context.Background())
	return res, rt, err
}

// checkpointBoundaries reports how many checkpoints a clean run of this graph
// writes, which is the number of places a process can be killed at.
func checkpointBoundaries(t *testing.T, g *ir.Graph, script map[string][]Completion, runID string, settleScript map[string]Settled) int {
	t.Helper()
	store := &jsonStore{}
	_, _, err := runToKill(t, g, script, Config{RunID: runID, Store: store}, store, settleScript)
	if err != nil {
		t.Fatalf("reference run: %v", err)
	}
	if store.saves < 2 {
		t.Fatalf("reference run wrote %d checkpoint(s); a resume test needs at least 2", store.saves)
	}
	return store.saves
}

func dispatchIDs(f *FakeRuntime) []string {
	var out []string
	for _, d := range f.Started() {
		out = append(out, d.DispatchID)
	}
	return out
}

// TestResumeAfterEveryKillSkipsCompletedNodes is the bead's central criterion:
// killing the process after each node and resuming completes the run without
// duplicate dispatches.
//
// The loop kills at EVERY checkpoint boundary, not only at node completions,
// because the interesting boundary is the tight one: a kill between an attempt's
// start and its completion, or between a completion and the next start. A resume
// must then either continue from the recorded completion or re-dispatch the one
// attempt that never reported — never re-dispatch a node the record shows as
// done, and never mint a dispatch id twice.
func TestResumeAfterEveryKillSkipsCompletedNodes(t *testing.T) {
	g := graphFromYAML(t, strings.Replace(chainYAML, "%s", "resume-chain", 1))
	script := okScript("steps.a", "steps.b", "steps.c")
	const runID = "run-chain"
	saves := checkpointBoundaries(t, g, script, runID, nil)

	sawInterrupted, sawFinishedRecord := false, false
	for k := 1; k <= saves; k++ {
		t.Run(fmt.Sprintf("kill-at-%d", k), func(t *testing.T) {
			store := &jsonStore{killAt: k}
			_, fake1, err := runToKill(t, g, script, Config{RunID: runID, Store: store}, store, nil)
			if err != nil && !errors.Is(err, errProcessKilled) {
				t.Fatalf("killed run returned %v, want the kill", err)
			}
			if !store.killed {
				t.Fatalf("kill at %d never fired (run wrote fewer checkpoints)", k)
			}
			cp, lerr := store.last(runID)
			if errors.Is(lerr, ErrRunNotFound) {
				// Killed before the first checkpoint landed: there is nothing to
				// resume, which is the honest answer rather than an invented one.
				return
			}
			if lerr != nil {
				t.Fatalf("last checkpoint: %v", lerr)
			}
			if !cp.FinishedAt.IsZero() {
				// The kill landed after the run's own close: a finished run is not
				// resumable.
				sawFinishedRecord = true
				_, _, rerr := resumeOver(t, g, script, store, runID, "", nil)
				if !errors.Is(rerr, ErrRunAlreadyFinished) {
					t.Fatalf("resume of a finished run = %v, want ErrRunAlreadyFinished", rerr)
				}
				return
			}
			done := map[string]bool{}
			for _, n := range cp.Nodes {
				if n == nil {
					continue
				}
				if n.State == StateSucceeded {
					done[n.Path] = true
				}
				if a := n.LastAttempt(); a != nil && a.Status == AttemptStatusInterrupted {
					sawInterrupted = true
				}
				if n.State == StateRunning {
					// An attempt was in flight when the process died: resume
					// must reopen it and re-dispatch under a fresh id.
					sawInterrupted = true
				}
			}

			res, fake2, rerr := resumeOver(t, g, script, store, runID, "", nil)
			if rerr != nil {
				t.Fatalf("resume: %v (checkpoint had %d node(s))", rerr, len(cp.Nodes))
			}
			if res.Status != RunSucceeded {
				t.Fatalf("resumed status = %s reason=%q, want succeeded\nlog:\n%s",
					res.Status, res.Reason, eventLog(res))
			}
			// The bead's claim, stated precisely: a node the record shows as
			// completed is never dispatched again.
			for _, d := range fake2.Started() {
				if done[d.Node.Path] {
					t.Errorf("resumed run re-dispatched %s, which the checkpoint recorded succeeded", d.Node.Path)
				}
			}
			// And no dispatch id is minted twice across the two processes, so a
			// trace joining requests and completions by id cannot merge two
			// different attempts into one.
			seen := map[string]bool{}
			for _, f := range []*FakeRuntime{fake1, fake2} {
				for _, id := range dispatchIDs(f) {
					if seen[id] {
						t.Errorf("dispatch id %s was minted twice", id)
					}
					seen[id] = true
				}
			}
			for _, p := range []string{"steps.a", "steps.b", "steps.c"} {
				if n := nodeOf(t, res, p); n.State != StateSucceeded {
					t.Errorf("%s = %s, want succeeded", p, n.State)
				}
			}
			// The event log is one sequence: the resumed run appended to the
			// killed run's log rather than starting a second one.
			if !strings.Contains(eventLog(res), "run_resumed") {
				t.Errorf("resumed run's log has no run_resumed line:\n%s", eventLog(res))
			}
			if got := strings.Count(eventLog(res), "run_started"); got != 1 {
				t.Errorf("log holds %d run_started line(s), want exactly 1 — a resume continues a run, it does not restart one", got)
			}
		})
	}
	if !sawInterrupted {
		t.Error("no case exercised an attempt that was in flight at the kill; the re-dispatch path is untested")
	}
	if !sawFinishedRecord {
		t.Error("no case exercised a kill after the run's own close; ErrRunAlreadyFinished is untested")
	}
}

// TestResumeSettlesProposalExactlyOnce is the other half of the criterion: a
// proposal created before a crash is settled exactly once after resume.
//
// Every kill boundary is exercised, including the one this bead adds machinery
// for — a kill between "the door was asked" and "the door answered". That one
// cannot be re-asked (the effect may have landed), so the resumed run fails the
// settle node with settlement_outcome_unknown and leaves the proposal open, which
// also stops the run from reporting success over it.
func TestResumeSettlesProposalExactlyOnce(t *testing.T) {
	g := graphFromYAML(t, settleChain("resume-settle", "select"))
	script := map[string][]Completion{"steps.work": {Proposal("prop-1", 0)}}
	const runID = "run-settle"
	saves := checkpointBoundaries(t, g, script, runID, nil)

	var sawRefusal, sawContinue bool
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
				return // finished before the kill: covered by the chain test
			}
			// What the record says about the proposal before resuming.
			recordedSettled, askedInFlight := false, false
			for _, n := range cp.Nodes {
				if n == nil {
					continue
				}
				if n.Asked != "" {
					askedInFlight = true
				}
				for _, s := range n.Settled {
					if s.ProposalID == "prop-1" && s.Status == SettlementStatusSettled {
						recordedSettled = true
					}
				}
			}

			res, fake2, rerr := resumeOver(t, g, script, store, runID, "", nil)
			if rerr != nil {
				t.Fatalf("resume: %v", rerr)
			}
			asked := len(fake1.Settles()) + len(fake2.Settles())
			if asked > 1 {
				t.Fatalf("the door was asked to settle %d time(s); a proposal has at most one disposition", asked)
			}
			var settled int
			for _, n := range res.Nodes {
				for _, s := range n.Settled {
					if s.Status == SettlementStatusSettled {
						settled++
					}
				}
			}
			switch {
			case askedInFlight:
				sawRefusal = true
				// The intent is recorded before the door is asked, so the kill
				// lands between the record and the ask: the door was never
				// asked, and the resume must not ask either — asking now could
				// double the one disposition a real crash might have delivered.
				if asked != 0 {
					t.Fatalf("door asked %d time(s) with an unanswered ask on the record, want 0 (never re-ask)", asked)
				}
				if settled != 0 {
					t.Fatalf("recorded %d settlement(s) for a disposition whose outcome the door never reported", settled)
				}
				land := nodeOf(t, res, "steps.land")
				if land.State != StateFailed || !strings.Contains(land.Reason, ReasonSettlementInterrupted) {
					t.Fatalf("land = %s (%q), want failed with %s", land.State, land.Reason, ReasonSettlementInterrupted)
				}
				if res.Status == RunSucceeded {
					t.Fatal("run reported success with a proposal whose disposition is unknown")
				}
			case recordedSettled:
				sawContinue = true
				if asked != 1 {
					t.Fatalf("door asked %d time(s) for a proposal already settled on the record, want 1", asked)
				}
				if settled != 1 {
					t.Fatalf("settled %d disposition(s), want exactly 1", settled)
				}
				if res.Status != RunSucceeded {
					t.Fatalf("status = %s reason=%q, want succeeded", res.Status, res.Reason)
				}
			default:
				sawContinue = true
				if asked != 1 {
					t.Fatalf("door asked %d time(s), want exactly 1", asked)
				}
				if settled != 1 {
					t.Fatalf("settled %d disposition(s), want exactly 1 (status %s reason %q)", settled, res.Status, res.Reason)
				}
				if res.Status != RunSucceeded {
					t.Fatalf("status = %s reason=%q, want succeeded", res.Status, res.Reason)
				}
			}
			// Whatever the run decided, the proposal is not sealed twice: the
			// sealing node's completion is recorded once, and the handle the
			// settle node read is the same id.
			work := nodeOf(t, res, "steps.work")
			if work.State != StateSucceeded || work.Proposal == nil || work.Proposal.ProposalID != "prop-1" {
				t.Fatalf("work = %s handle=%+v, want succeeded with prop-1", work.State, work.Proposal)
			}
			// The producer may legitimately be re-dispatched only when the kill
			// landed before its completion was durably recorded (the default
			// branch: work was ready or in-flight, so resume re-ran it). Once
			// work's completion is on the record — which both askedInFlight and
			// recordedSettled imply — it must not run again.
			if (askedInFlight || recordedSettled) && len(work.Attempts) != 1 {
				t.Fatalf("work holds %d attempt(s), want 1 — the proposal's producer must not be re-dispatched once completed", len(work.Attempts))
			}
			if !askedInFlight && !recordedSettled && len(work.Attempts) < 1 {
				t.Fatalf("work holds %d attempt(s), want at least 1", len(work.Attempts))
			}
		})
	}
	if !sawContinue {
		t.Error("no case exercised a resume that carries a settlement forward")
	}
	if !sawRefusal {
		t.Error("no case exercised an unanswered settlement ask; the exactly-once refusal is untested")
	}
}

// TestResumeRestoresFanoutInstances pins that a container's body instances are
// rebuilt, not re-expanded: a fan-out killed mid-flight resumes the items it
// minted and dispatches only the instances the record does not show as done.
func TestResumeRestoresFanoutInstances(t *testing.T) {
	g := graphFromYAML(t, fanoutYAML("resume-fanout", "[a, b, c]", ""))
	script := map[string][]Completion{
		itemPathOf("steps.f", 0, 0): {Report("one", 0)},
		itemPathOf("steps.f", 1, 0): {Report("two", 0)},
		itemPathOf("steps.f", 2, 0): {Report("three", 0)},
	}
	const runID = "run-fanout"
	saves := checkpointBoundaries(t, g, script, runID, nil)

	// A boundary in the middle of the expansion is the interesting one: by then
	// the items exist as state and the instances must come back from the record.
	k := saves / 2
	if k < 2 {
		k = 2
	}
	store := &jsonStore{killAt: k}
	_, fake1, err := runToKill(t, g, script, Config{RunID: runID, Store: store}, store, nil)
	if err != nil && !errors.Is(err, errProcessKilled) {
		t.Fatalf("killed run returned %v", err)
	}
	cp, lerr := store.last(runID)
	if lerr != nil {
		t.Fatalf("last checkpoint: %v", lerr)
	}
	if len(cp.Instances) == 0 {
		t.Fatalf("checkpoint after %d save(s) holds no instances; the fan-out had not expanded yet", k)
	}
	res, fake2, rerr := resumeOver(t, g, script, store, runID, "", nil)
	if rerr != nil {
		t.Fatalf("resume: %v", rerr)
	}
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, eventLog(res))
	}
	f := nodeOf(t, res, "steps.f")
	if f.State != StateSucceeded || len(f.Items) != 3 || len(f.Collect) != 3 {
		t.Fatalf("fan-out = %s items=%d collect=%d, want succeeded with 3 of each", f.State, len(f.Items), len(f.Collect))
	}
	got, _ := json.Marshal(f.Collect)
	if string(got) != `["one","two","three"]` {
		t.Errorf("collect = %s, want the item products in input order", got)
	}
	done := map[string]bool{}
	for _, n := range cp.Nodes {
		if n != nil && n.State == StateSucceeded {
			done[n.Path] = true
		}
	}
	seen := map[string]bool{}
	for _, f := range []*FakeRuntime{fake1, fake2} {
		for _, id := range dispatchIDs(f) {
			if seen[id] {
				t.Errorf("dispatch id %s was minted twice", id)
			}
			seen[id] = true
		}
	}
	for _, d := range fake2.Started() {
		if done[d.Node.Path] {
			t.Errorf("resumed run re-dispatched %s, which the checkpoint recorded succeeded", d.Node.Path)
		}
	}
}

// TestResumeRefusesSpecHashMismatch pins the refusal: a run's record names the
// document it was started from, and a resume against a different one is refused
// rather than applied to a graph the state was never built for.
func TestResumeRefusesSpecHashMismatch(t *testing.T) {
	g := graphFromYAML(t, strings.Replace(chainYAML, "%s", "resume-hash", 1))
	script := okScript("steps.a", "steps.b", "steps.c")
	store := &jsonStore{}
	if _, _, err := runToKill(t, g, script, Config{RunID: "run-hash", SpecHash: "aaa", Store: store},
		&jsonStore{}, nil); err != nil {
		t.Fatalf("reference run: %v", err)
	}
	cp, err := store.last("run-hash")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if cp.SpecHash != "aaa" {
		t.Fatalf("persisted spec hash = %q, want aaa", cp.SpecHash)
	}
	// A run that finished cannot be resumed either; both refusals are checked
	// against a record taken mid-run, so build one with a kill.
	mid := &jsonStore{killAt: 2}
	if _, _, err := runToKill(t, g, script, Config{RunID: "run-hash", SpecHash: "aaa", Store: mid}, mid, nil); !errors.Is(err, errProcessKilled) {
		t.Fatalf("killed run = %v", err)
	}
	cp, err = mid.last("run-hash")
	if err != nil {
		t.Fatalf("mid-run checkpoint: %v", err)
	}
	rt := NewFakeRuntime(time.Unix(0, 0))
	rt.Script = script
	if _, err := Resume(g, rt, Config{Store: mid, SpecHash: "bbb"}, cp); !errors.Is(err, ErrSpecHashMismatch) {
		t.Fatalf("resume against another document = %v, want ErrSpecHashMismatch", err)
	}
	// And the same record resumes when the hash agrees.
	rt2 := NewFakeRuntime(time.Unix(0, 0))
	rt2.Script = script
	if _, err := Resume(g, rt2, Config{Store: mid, SpecHash: "aaa"}, cp); err != nil {
		t.Fatalf("resume against the same document: %v", err)
	}
}

// TestResumeRefusesUnknownRun pins the missing-record case: a run id the store
// has never seen is ErrRunNotFound, not an empty run.
func TestResumeRefusesUnknownRun(t *testing.T) {
	g := graphFromYAML(t, strings.Replace(chainYAML, "%s", "resume-missing", 1))
	store := &jsonStore{}
	_, err := store.LoadCheckpoint(context.Background(), "nope")
	if !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("LoadCheckpoint = %v, want ErrRunNotFound", err)
	}
	rt := NewFakeRuntime(time.Unix(0, 0))
	if _, err := Resume(g, rt, Config{Store: store}, Checkpoint{}); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("Resume with an empty checkpoint = %v, want ErrRunNotFound", err)
	}
}

// TestCheckpointReflectsCommittedState pins the projection's honesty: the
// snapshot a store receives is the state Apply committed, including the attempt
// that is currently open, the proposal handle, and the ledger.
func TestCheckpointReflectsCommittedState(t *testing.T) {
	g := graphFromYAML(t, settleChain("checkpoint-shape", "discard"))
	store := &jsonStore{}
	r, _, err := runToKill(t, g, map[string][]Completion{"steps.work": {Proposal("prop-shape", 0)}},
		Config{RunID: "run-shape", SpecHash: "hash-shape", SpecPath: "spec.yaml", Store: store}, store, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	cp, err := store.last("run-shape")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if cp.RunID != "run-shape" || cp.SpecHash != "hash-shape" || cp.SpecPath != "spec.yaml" || cp.GraphName != "checkpoint-shape" {
		t.Errorf("checkpoint identity = %+v, want the run id, hash, path, and graph name", cp)
	}
	if cp.Status != RunSucceeded || cp.FinishedAt.IsZero() {
		t.Errorf("checkpoint status = %s finished=%v, want succeeded with a finish time", cp.Status, cp.FinishedAt)
	}
	if cp.Dispatches != 1 {
		t.Errorf("dispatches = %d, want 1", cp.Dispatches)
	}
	if cp.Seq != len(cp.Events) {
		t.Errorf("seq = %d with %d event line(s) restored, want them equal", cp.Seq, len(cp.Events))
	}
	byPath := map[string]*NodeState{}
	for _, n := range cp.Nodes {
		byPath[n.Path] = n
	}
	work := byPath["steps.work"]
	if work == nil || work.Proposal == nil || work.Proposal.ProposalID != "prop-shape" {
		t.Fatalf("work node = %+v, want the sealed proposal handle", work)
	}
	if len(work.Attempts) != 1 || work.Attempts[0].DispatchID != "steps.work#1" {
		t.Fatalf("attempts = %+v, want one attempt under steps.work#1", work.Attempts)
	}
	if len(cp.Ledger.Settled) != 1 || cp.Ledger.Settled["prop-shape"] != "steps.land" {
		t.Errorf("ledger settled = %+v, want prop-shape disposed of by steps.land", cp.Ledger.Settled)
	}
	if len(cp.Ledger.Open) != 0 {
		t.Errorf("ledger open = %+v, want nothing left open", cp.Ledger.Open)
	}
	if want := r.Events(); len(want) != cp.Seq {
		t.Errorf("live log has %d line(s), checkpoint recorded %d", len(want), cp.Seq)
	}
}
