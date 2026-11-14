package exec

// Durable run state (yaah-0u0v.16).
//
// A long fan-out can be killed, and re-running one from scratch would duplicate
// dispatches and — worse — settlements. So the reducer's state is persisted in
// the session store family keyed by run_id, and a resumed run continues from it.
//
// # One authoritative owner
//
// The in-memory Run IS the model: NodeState is the authority, and the durable
// projection is a snapshot of it, not a peer database. There is no second state
// machine here — a checkpoint is produced by reading the reducer, and a resume
// installs a reducer state from a checkpoint. The event log that accompanies it
// is a projection too (the trace bead yaah-0u0v.17 reads it), which is why the
// log is carried inside the same checkpoint rather than appended to some other
// table.
//
// # Why the checkpoint is written inside Apply
//
// Apply is the reducer's only mutator, and it persists after folding the event
// and before returning. That single sentence is the whole idempotency argument:
//
//   - an attempt exists in the record only after its dispatch_started event is
//     folded, and the run cannot start the next attempt until Apply returned, so
//     a crash cannot leave an attempt the record does not know about;
//   - a proposal handle is recorded by the same fold that records the attempt's
//     completion, so a sealed proposal can never be lost between "sealed" and
//     "known";
//   - a settlement's INTENT is recorded by its own event before the door is
//     called (settlement_asked), so a resume can tell "asked, outcome unknown"
//     from "never asked" and refuses to ask a second time.
//
// # What a resume refuses
//
// A run that already finished is not resumable (its disposition is final), and a
// run whose persisted spec hash disagrees with the graph in hand is refused:
// continuing it would apply a state machine to a document that has changed
// underneath it.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/buchenberg/yaah/internal/workflow/ir"
	"github.com/buchenberg/yaah/internal/workflow/trace"
)

// Errors a resume reports. They are distinct because the caller's response
// differs: a missing run is "wrong run id", a finished run is "nothing to do",
// and a hash mismatch is "the document changed".
var (
	// ErrRunNotFound is reported by a Store that holds no checkpoint for the
	// requested run id.
	ErrRunNotFound = errors.New("workflow/exec: no persisted run")
	// ErrSpecHashMismatch refuses a resume against a document other than the one
	// the run was started from.
	ErrSpecHashMismatch = errors.New("workflow/exec: spec hash mismatch")
	// ErrRunAlreadyFinished refuses a resume of a run whose disposition is
	// already final.
	ErrRunAlreadyFinished = errors.New("workflow/exec: run already finished")
)

// AttemptStatusInterrupted marks an attempt the run was killed during. It is not
// an envelope status — no lane reports it — which is exactly why it is worth
// recording: the attempt history shows work that was started and whose outcome
// the world never told us.
const AttemptStatusInterrupted = "interrupted"

// ReasonSettlementInterrupted is the refusal a reset settle node states when a
// disposition was asked for and the run stopped before the door answered. The
// proposal is left open, and the run cannot report success while it is, so the
// gap is visible instead of being papered over with a second ask.
const ReasonSettlementInterrupted = "settlement_outcome_unknown"

// Ledger is the run's proposal bookkeeping: the proposals it sealed and has not
// disposed of, the ones a settle node is working through, and the ones that
// reached a disposition. It is the reducer's own state, carried in the
// checkpoint so a resumed run's linearity guards read the same ledger the
// original run had (yaah-0u0v.13's guards, extended by .16).
type Ledger struct {
	Open    map[string]string `json:"open,omitempty"`
	Claims  map[string]string `json:"claims,omitempty"`
	Settled map[string]string `json:"settled,omitempty"`
}

// InstanceState is one body instance's identity, recorded so a resume can
// rebuild the instance without re-running the expansion that minted it. The body
// TEMPLATE is not persisted: BodyPath names the container's body node the
// instance was copied from, and the resume copies it again from the same
// immutable graph.
type InstanceState struct {
	Path     string            `json:"path"`
	Owner    string            `json:"owner"`
	BodyPath string            `json:"body_path"`
	Item     int               `json:"item,omitempty"`
	Name     string            `json:"name,omitempty"`
	Bound    any               `json:"bound,omitempty"`
	Locals   map[string]string `json:"locals,omitempty"`
}

// Checkpoint is the durable projection of one run at one instant: everything the
// reducer had committed when Apply last returned.
type Checkpoint struct {
	RunID string `json:"run_id"`
	// SpecHash and SpecPath identify the document the run was started from.
	SpecHash string `json:"spec_hash,omitempty"`
	SpecPath string `json:"spec_path,omitempty"`
	// GraphName is the document's metadata.name, kept so a listing can say which
	// workflow a run id belongs to without resolving the document.
	GraphName string    `json:"graph_name,omitempty"`
	Status    RunStatus `json:"status"`
	Reason    string    `json:"reason,omitempty"`
	// Dispatches and PeakInFlight are the run's tallies so far: a resumed run
	// keeps spending the same budget, and the concurrency ceiling is measured
	// against the same dispatches.
	Dispatches   int `json:"dispatches"`
	PeakInFlight int `json:"peak_in_flight,omitempty"`

	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`

	// Params are the parameters the run bound at New. A resume restores them
	// rather than re-binding from the caller: templates already rendered against
	// these values, and a run resumed under different params would be a
	// different run with the same id.
	Params map[string]any `json:"params,omitempty"`
	Ledger Ledger         `json:"ledger"`
	// Seq is the last event sequence number the run wrote.
	Seq int `json:"seq"`

	// Nodes is every node's state in canonical result order — a top-level node
	// followed by the instances its containers minted. Instances describes those
	// instance paths, in the same order.
	Nodes     []*NodeState    `json:"nodes"`
	Instances []InstanceState `json:"instances,omitempty"`
	Warnings  []string        `json:"warnings,omitempty"`

	// EventBase is the number of log lines the store has already recorded, so a
	// save writes only what is new. Events are those new lines, oldest first.
	EventBase int      `json:"event_base,omitempty"`
	Events    []string `json:"events,omitempty"`

	// TraceBase and Trace are the same pair for the run's trace records
	// (yaah-0u0v.17): the count the store already holds, and the new records in
	// sequence order. A record is a typed projection of one event line, so the
	// two append in lockstep and a resumed run's trace continues the sequence
	// instead of restarting it.
	TraceBase int            `json:"trace_base,omitempty"`
	Trace     []trace.Record `json:"trace,omitempty"`
}

// Store is the durable projection of a run: the one place its state is kept
// between processes. A store must be idempotent — the reducer re-writes the
// whole projection on every event — and it must be atomic per call, because a
// half-written checkpoint is a run state that never existed.
//
// The port lives here rather than in the store's own package so this package
// depends on nothing: internal/workflow/store is the SQLite adapter over the
// session store family, and a test supplies an in-memory one.
type Store interface {
	// SaveCheckpoint records the run's projection. A save that fails stops the
	// run: state that cannot be recorded must not advance.
	SaveCheckpoint(ctx context.Context, cp Checkpoint) error
	// LoadCheckpoint returns the last recorded projection for a run id, or an
	// error wrapping ErrRunNotFound.
	LoadCheckpoint(ctx context.Context, runID string) (Checkpoint, error)
	// Close releases the store. A caller that opened the store is responsible
	// for closing it; on Windows an open SQLite file cannot be removed.
	Close() error
}

// checkpoint writes the run's projection when a Store is configured. It is
// called from Apply — and only from Apply, plus the explicit write a Resume makes
// after installing its state — so the record never runs ahead of the reducer and
// the reducer never runs ahead of the record.
func (r *Run) checkpoint() error {
	if r.cfg.Store == nil {
		return nil
	}
	cp := r.Checkpoint()
	cp.EventBase = r.persisted
	cp.TraceBase = r.recordsPersisted
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := r.cfg.Store.SaveCheckpoint(ctx, cp); err != nil {
		return fmt.Errorf("workflow/exec: persisting run %s: %w", r.runID, err)
	}
	r.persisted = len(r.events)
	r.recordsPersisted = len(r.records)
	return nil
}

// Checkpoint returns the run's current projection. It is a snapshot: node states
// are copied, and attempt records are shared because a committed attempt is
// immutable.
func (r *Run) Checkpoint() Checkpoint {
	cp := Checkpoint{
		RunID:        r.runID,
		SpecHash:     r.specHash,
		SpecPath:     r.specPath,
		Status:       r.status,
		Reason:       r.reason,
		Dispatches:   r.dispatches,
		PeakInFlight: r.peakInFlight,
		StartedAt:    r.startedAt,
		FinishedAt:   r.finishedAt,
		Params:       copyAnyMap(r.params),
		Ledger: Ledger{
			Open:    copyStringMap(r.open),
			Claims:  copyStringMap(r.claims),
			Settled: copyStringMap(r.settled),
		},
		Seq:      r.seq,
		Warnings: append([]string(nil), r.warnings...),
		Events:   append([]string(nil), r.events[r.newEvents():]...),
		Trace:    append([]trace.Record(nil), r.records[r.newRecords():]...),
	}
	if r.graph != nil {
		cp.GraphName = r.graph.Name
	}
	for _, p := range r.allPaths() {
		cp.Nodes = append(cp.Nodes, copyNodeState(r.state[p]))
		if inst := r.instances[p]; inst != nil {
			cp.Instances = append(cp.Instances, InstanceState{
				Path:     p,
				Owner:    inst.owner,
				BodyPath: inst.body,
				Item:     inst.item,
				Name:     inst.name,
				Bound:    inst.bound,
				Locals:   copyStringMap(inst.locals),
			})
		}
	}
	return cp
}

// newEvents is the index of the first log line the store has not recorded. It is
// clamped so a caller that rewinds the marker cannot make the snapshot slice out
// of range.
func (r *Run) newEvents() int {
	if r.persisted < 0 {
		return 0
	}
	if r.persisted > len(r.events) {
		return len(r.events)
	}
	return r.persisted
}

// newRecords is newEvents for the trace projection: the index of the first record
// the store has not recorded, clamped the same way.
func (r *Run) newRecords() int {
	if r.recordsPersisted < 0 {
		return 0
	}
	if r.recordsPersisted > len(r.records) {
		return len(r.records)
	}
	return r.recordsPersisted
}

// Resume rebuilds a run from a checkpoint and returns it ready to drive: the
// nodes that had terminated stay terminated, the ones that were waiting stay
// waiting, and the next Advance continues from there.
//
// The graph must be the one the checkpoint's SpecHash names; cfg.SpecHash is
// compared against the record and a mismatch is refused with
// ErrSpecHashMismatch. cfg.Store is used for the resume's own writes, and
// cfg.RunID is ignored in favour of the checkpoint's: a run id is the identity of
// a run, not an argument to it.
func Resume(g *ir.Graph, rt Runtime, cfg Config, cp Checkpoint) (*Run, error) {
	if g == nil {
		return nil, errors.New("workflow/exec: nil graph")
	}
	if rt == nil {
		return nil, errors.New("workflow/exec: nil Runtime")
	}
	if cp.RunID == "" {
		return nil, fmt.Errorf("%w: checkpoint has no run id", ErrRunNotFound)
	}
	// A disposition is final. Anything else the record states (including a budget
	// breach the close has not performed yet) is resumable: the next Advance
	// finishes the close.
	if !cp.FinishedAt.IsZero() {
		return nil, fmt.Errorf("%w: run %s ended %s (%s)", ErrRunAlreadyFinished, cp.RunID, cp.Status, cp.Reason)
	}
	switch cp.Status {
	case "", RunRunning, RunBudgetExceeded:
	default:
		return nil, fmt.Errorf("%w: run %s ended %s (%s)", ErrRunAlreadyFinished, cp.RunID, cp.Status, cp.Reason)
	}
	// SpecHash is the caller's declaration of the document it resolved. When a
	// caller records a hash and the checkpoint records one, they must agree;
	// an empty hash on either side declines the comparison, because a one-shot
	// run (or a test double) may never have recorded one. Callers that want the
	// mismatch refused must set Config.SpecHash — the CLI always does.
	if hs := cfg.SpecHash; hs != "" && cp.SpecHash != "" && hs != cp.SpecHash {
		return nil, fmt.Errorf("%w: run %s was started from spec %s but the document in hand hashes to %s",
			ErrSpecHashMismatch, cp.RunID, cp.SpecHash, hs)
	}
	// The run's own params win: New must bind them, or every template that
	// already rendered against them would be re-resolved under other values.
	cfg.Params = copyAnyMap(cp.Params)
	cfg.RunID = cp.RunID
	r, err := New(g, rt, cfg)
	if err != nil {
		return nil, err
	}
	// New minted a run id from cfg.RunID, which the checkpoint overrides anyway.
	r.runID = cp.RunID
	if r.specHash == "" {
		r.specHash = cp.SpecHash
	}
	if r.specPath == "" {
		r.specPath = cp.SpecPath
	}
	if err := r.install(cp); err != nil {
		return nil, err
	}
	// The resume itself is a logged fact, written before anything else happens so
	// the log says where the process boundary was.
	if err := r.Apply(RunResumedEvent{RunID: r.runID, Nodes: len(cp.Nodes), Instances: len(cp.Instances)}); err != nil {
		return nil, err
	}
	// A settle node whose disposition was asked for and never answered is
	// discharged NOW, before any further work: asking again is the one thing this
	// bead exists to prevent. The failure leaves the proposal open, so the run
	// cannot report success over it.
	//
	// A join:any|quorum container's discard (yaah-0u0v.19) is the same fact seen
	// from an already-terminated node: the container's outcome is final and cannot
	// be re-decided, so the unknown outcome is recorded on its ledger, the ask is
	// cleared, and the proposal stays open — which keeps the run from reporting
	// success over it while never asking for one disposition twice.
	for _, p := range r.allPaths() {
		st := r.state[p]
		if st.Asked == "" {
			continue
		}
		if st.State != StateReady {
			// Only a decided join container discharges its unanswered discard
			// into Discards: that field's contract is join residue. A settle
			// node interrupted after its ask keeps its prior handling — the ask
			// is left set and the run that cancelled it reports the open
			// proposal through the ledger, never by re-asking.
			if n := r.byPath[p]; n != nil && n.Kind == ir.KindFanout && cancelJoin(n) {
				r.warnOnce("join-discard-unknown:"+p, fmt.Sprintf(
					"%s: a discard of proposal %s was asked for before the run stopped and the door never reported an outcome; refusing to ask a second time (a proposal has exactly one disposition), so the proposal stays open",
					p, st.Asked))
				st.Discards = append(st.Discards, Settled{
					ProposalID: st.Asked,
					Status:     ReasonSettlementInterrupted,
					Error:      "asked for before the run stopped; the door never answered",
					LeftOpen:   true,
				})
				st.Asked = ""
			}
			continue
		}
		if err := r.failTerminal(p, fmt.Sprintf("%s: proposal %s was asked for before the run stopped and the door never reported an outcome; refusing to ask a second time (a proposal has exactly one disposition)",
			ReasonSettlementInterrupted, st.Asked)); err != nil {
			return nil, err
		}
	}
	if err := r.checkpoint(); err != nil {
		return nil, err
	}
	return r, nil
}

// install folds a checkpoint's state into a freshly built run. It touches only
// what the checkpoint records: the graph-derived topology (paths, ids) stays as
// New built it, because the document is the same document.
func (r *Run) install(cp Checkpoint) error {
	r.started = true // run_started was folded before the checkpoint was written
	r.status = cp.Status
	if r.status == "" {
		r.status = RunRunning
	}
	// A non-running status on a run that has not finished can only be a budget
	// breach: it is the one event that sets a disposition without closing the run,
	// and the close that follows it is what the next Advance performs.
	r.stopping = r.status != RunRunning
	r.reason = cp.Reason
	r.dispatches = cp.Dispatches
	r.peakInFlight = cp.PeakInFlight
	r.startedAt = cp.StartedAt
	r.finishedAt = cp.FinishedAt
	r.warnings = append([]string(nil), cp.Warnings...)
	r.events = append([]string(nil), cp.Events...)
	r.records = append([]trace.Record(nil), cp.Trace...)
	r.seq = cp.Seq
	if r.seq < len(r.events) {
		r.seq = len(r.events)
	}
	r.persisted = len(r.events)
	r.recordsPersisted = len(r.records)
	r.open = copyStringMap(cp.Ledger.Open)
	r.claims = copyStringMap(cp.Ledger.Claims)
	r.settled = copyStringMap(cp.Ledger.Settled)

	// Instances first: they are recorded in canonical result order, so a
	// container instance exists before the body instances it minted.
	for _, in := range cp.Instances {
		if err := r.installInstance(in); err != nil {
			return err
		}
	}
	for _, n := range cp.Nodes {
		if _, ok := r.byPath[n.Path]; !ok {
			return fmt.Errorf("workflow/exec: checkpoint for run %s holds node %q, which this graph does not declare",
				cp.RunID, n.Path)
		}
		restored := copyNodeState(n)
		if restored.State == StateRunning {
			r.reopenInterrupted(restored)
		}
		r.state[n.Path] = restored
	}
	// The topology is rebuilt from the restored nodes rather than trusted from
	// the checkpoint: needs resolve per scope, and an instance's needs resolve
	// inside its own item first.
	r.rebuildTopology()
	// The ready queue is state, not an event: a node the checkpoint recorded
	// ready was ready, and it is startable again the moment the run advances.
	for _, p := range r.allPaths() {
		if st := r.state[p]; st.State == StateReady {
			r.insertReady(p)
		}
	}
	return nil
}

// installInstance re-mints one body instance from its recorded identity and the
// body template it names.
func (r *Run) installInstance(in InstanceState) error {
	owner := r.byPath[in.Owner]
	if owner == nil {
		return fmt.Errorf("workflow/exec: instance %q names container %q, which is not in the graph", in.Path, in.Owner)
	}
	var tpl *ir.Node
	for _, b := range owner.Body {
		if b != nil && b.Path == in.BodyPath {
			tpl = b
			break
		}
	}
	if tpl == nil {
		return fmt.Errorf("workflow/exec: instance %q names body %q, which container %q does not declare",
			in.Path, in.BodyPath, in.Owner)
	}
	ic := *tpl
	ic.Path = in.Path
	r.byPath[in.Path] = &ic
	r.instances[in.Path] = &instance{
		owner:  in.Owner,
		item:   in.Item,
		bound:  in.Bound,
		name:   in.Name,
		body:   in.BodyPath,
		locals: copyStringMap(in.Locals),
	}
	r.instanceOf[in.Owner] = append(r.instanceOf[in.Owner], in.Path)
	return nil
}

// reopenInterrupted returns a node the run was killed during to the ready queue
// and marks the attempt that never reported. The attempt is NOT a failure and is
// not counted as one: the run asked, the world never answered, and the work is
// re-dispatched under a new attempt number so the recorded dispatch ids stay
// unique.
func (r *Run) reopenInterrupted(st *NodeState) {
	if a := st.LastAttempt(); a != nil {
		a.Status = AttemptStatusInterrupted
		a.StopReason = AttemptStatusInterrupted
		r.warnOnce("interrupted:"+st.Path, fmt.Sprintf(
			"%s: attempt %d was in flight when the run stopped; it is re-dispatched as a new attempt (no completion was ever recorded)",
			st.Path, a.Number))
	}
	st.State = StateReady
}

// rebuildTopology recomputes the dependency and routing relations from the
// restored node set, exactly as New and instantiateBody build them.
func (r *Run) rebuildTopology() {
	r.dependents = map[string][]string{}
	for _, p := range r.allPaths() {
		needs, ok := r.needPaths(r.byPath[p])
		if !ok {
			continue // a need naming no step strands the node; reach.go reports it as WF004
		}
		for _, dep := range needs {
			r.dependents[dep] = append(r.dependents[dep], p)
		}
	}
	for dep := range r.dependents {
		sort.Strings(r.dependents[dep])
	}
	r.arms = map[string]map[string]bool{}
	for _, p := range r.allPaths() {
		r.registerArms(r.byPath[p])
	}
}

// RunResumedEvent records that a process picked a run up from its checkpoint. It
// is an ordinary input event so the resume is folded — and persisted — like every
// other fact, and so the log says where one process ended and the next began.
type RunResumedEvent struct {
	RunID string
	// Nodes and Instances are the counts the checkpoint restored; they let a
	// reader tell a resume of a large run from one that restored nothing.
	Nodes     int
	Instances int
}

func (RunResumedEvent) Kind() EventKind  { return EventRunResumed }
func (e RunResumedEvent) target() string { return "" }

// onRunResumed validates the one-fact-per-process rule: a Run resumes once. It
// deliberately leaves the status, the stopping flag, and the tallies exactly as
// the checkpoint stated them — a resumed run continues the run it was, including
// a budget breach the close has not performed yet.
func (r *Run) onRunResumed(e RunResumedEvent) error {
	if r.resumed {
		return errors.New("workflow/exec: run_resumed applied twice")
	}
	r.resumed = true
	if e.RunID != "" {
		r.runID = e.RunID
	}
	return nil
}

func copyNodeState(st *NodeState) *NodeState {
	if st == nil {
		return nil
	}
	out := *st
	out.Needs = append([]string(nil), st.Needs...)
	out.Attempts = append([]*Attempt(nil), st.Attempts...)
	out.Exposed = append([]string(nil), st.Exposed...)
	out.Outputs = append([]NodeOutput(nil), st.Outputs...)
	out.Settled = append([]Settled(nil), st.Settled...)
	out.Discards = append([]Settled(nil), st.Discards...)
	out.Items = append([]FanoutItem(nil), st.Items...)
	out.Winners = append([]int(nil), st.Winners...)
	out.Iterations = append([]LoopIteration(nil), st.Iterations...)
	out.Collect = append([]any(nil), st.Collect...)
	out.Candidates = append([]Candidate(nil), st.Candidates...)
	return &out
}

func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
