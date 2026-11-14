package memory

// Durable workflow-run state (yaah-0u0v.16).
//
// A workflow run is a long-lived thing: a fan-out can dispatch for minutes, and
// re-running one from scratch would duplicate dispatches and — worse — settlements.
// So the scheduler's state is projected into this store, keyed by run_id, and
// `yaah workflow resume` continues from it.
//
// # Why these tables live here
//
// They live in the session store family (this package's SQLite database, its
// migrations, its pragmas) rather than in a second database or a new migration
// system, which is the bead's hard constraint. Nothing here is a peer of the
// scheduler's state: a row is a serialized snapshot of what
// internal/workflow/exec had committed, and the payload columns are opaque to
// this package — the adapter in internal/workflow/store owns their shape, so the
// general store never has to know what a fan-out item is.
//
// # Column versus payload
//
// The split is deliberate. Columns that a query has to filter or order by —
// state, run status, dispatch id, proposal id, ordinal — are real columns; the
// rest is one JSON payload per row. That keeps the schema stable across
// scheduler changes while still letting a resume ask "which nodes had
// terminated" without decoding anything.
//
// # Exactly-once settlement, as an invariant
//
// idx_workflow_settlements_settled is a UNIQUE index over (run_id, proposal_id)
// restricted to rows whose status is 'settled'. So two successful dispositions of
// one proposal in one run are refused by the database, not merely by the
// scheduler's ledger — a resumed run cannot double-settle even if its own
// bookkeeping were wrong. A conflicted or errored disposition IS allowed to
// coexist, because on_conflict: skip leaves the proposal open on purpose for a
// downstream catch settle to land.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrWorkflowRunNotFound reports that no run state is stored under that id.
var ErrWorkflowRunNotFound = errors.New("memory: workflow run not found")

// WorkflowRunRow is one run's own row: its identity, its document, and its
// disposition.
type WorkflowRunRow struct {
	RunID    string
	SpecHash string
	SpecPath string
	// GraphName is the document's metadata.name.
	GraphName string
	Status    string
	Reason    string
	// Dispatches and PeakInFlight are the run's tallies, so a resumed run keeps
	// spending the same budget.
	Dispatches   int
	PeakInFlight int
	// StartedAt and FinishedAt are unix nanoseconds; zero means unset.
	StartedAt  int64
	FinishedAt int64
	// Params and Ledger are JSON: the bound workflow parameters and the
	// proposal ledger (open/claims/settled).
	Params   string
	Ledger   string
	Warnings string
	// Seq is the last event sequence number the run wrote.
	Seq       int
	UpdatedAt int64
}

// WorkflowNodeRow is one node's state. Ordinal preserves the scheduler's
// canonical result order — a top-level node followed by the instances its
// containers minted — because a resume must rebuild that order, not a map's.
type WorkflowNodeRow struct {
	Path    string
	Kind    string
	State   string
	Ordinal int
	Payload string
}

// WorkflowInstanceRow is one body instance's identity: which container minted it,
// which body template it was copied from, and what its own scope bound.
type WorkflowInstanceRow struct {
	Path     string
	Owner    string
	BodyPath string
	Item     int
	Name     string
	Ordinal  int
	Payload  string
}

// WorkflowAttemptRow is one dispatched attempt. DispatchID is
// "<node path>#<number>", so the primary key is also the identity a trace joins
// the dispatch envelope on.
type WorkflowAttemptRow struct {
	DispatchID string
	NodePath   string
	Number     int
	Status     string
	StartedAt  int64
	FinishedAt int64
	Payload    string
}

// WorkflowSettlementRow is one settlement disposition a settle node recorded.
// Status is the proposal runtime's own verdict — settled, conflict, or error —
// verbatim.
type WorkflowSettlementRow struct {
	NodePath   string
	ProposalID string
	Producer   string
	Action     string
	Status     string
	RecordedAt int64
	Payload    string
}

// WorkflowEventRow is one line of the run's canonical event log. The log is a
// projection of the run, kept beside the state it describes so a resumed run's
// report and the trace bead (yaah-0u0v.17) read one sequence.
type WorkflowEventRow struct {
	Seq  int
	Line string
}

// WorkflowTraceRow is one typed trace record (yaah-0u0v.17). Seq is the
// record's own sequence number within the run, EventSeq the event log line it
// projects, and Kind, NodePath, Attempt and Status are columns because a trace
// reader filters on them ("every dispatch of this node", "every settle record")
// without decoding the payload.
type WorkflowTraceRow struct {
	Seq      int
	EventSeq int
	Kind     string
	NodePath string
	Attempt  int
	Status   string
	Payload  string
}

// WorkflowCheckpoint is one run's whole durable projection.
type WorkflowCheckpoint struct {
	Run         WorkflowRunRow
	Nodes       []WorkflowNodeRow
	Instances   []WorkflowInstanceRow
	Attempts    []WorkflowAttemptRow
	Settlements []WorkflowSettlementRow
	// Events are appended, never rewritten: each row is one sequence number.
	Events []WorkflowEventRow
	// Trace are the run's typed trace records, appended from the same events the
	// Events rows render (yaah-0u0v.17).
	Trace []WorkflowTraceRow
}

// SaveWorkflowCheckpoint writes one run's projection in a single transaction.
// Every statement is an upsert, so re-saving the same projection is a no-op:
// the scheduler re-writes the whole snapshot on every event, and a store that
// only tolerated deltas would have to trust a diff it cannot verify.
func (d *DB) SaveWorkflowCheckpoint(cp WorkflowCheckpoint) error {
	if cp.Run.RunID == "" {
		return errors.New("save workflow checkpoint: run id is required")
	}
	tx, err := d.sql.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin workflow checkpoint %s: %w", cp.Run.RunID, err)
	}
	defer tx.Rollback()
	run := cp.Run
	if _, err := tx.Exec(`
		INSERT INTO workflow_runs
			(run_id, spec_hash, spec_path, graph_name, status, reason, dispatches, peak_in_flight,
			 started_at, finished_at, params, ledger, warnings, seq, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET
			spec_hash = excluded.spec_hash,
			spec_path = excluded.spec_path,
			graph_name = excluded.graph_name,
			status = excluded.status,
			reason = excluded.reason,
			dispatches = excluded.dispatches,
			peak_in_flight = excluded.peak_in_flight,
			started_at = excluded.started_at,
			finished_at = excluded.finished_at,
			params = excluded.params,
			ledger = excluded.ledger,
			warnings = excluded.warnings,
			seq = excluded.seq,
			updated_at = excluded.updated_at`,
		run.RunID, run.SpecHash, run.SpecPath, run.GraphName, run.Status, run.Reason,
		run.Dispatches, run.PeakInFlight, run.StartedAt, run.FinishedAt,
		orEmptyJSON(run.Params, "{}"), orEmptyJSON(run.Ledger, "{}"), orEmptyJSON(run.Warnings, "[]"),
		run.Seq, run.UpdatedAt); err != nil {
		return fmt.Errorf("save workflow run %s: %w", run.RunID, err)
	}
	for _, n := range cp.Nodes {
		if _, err := tx.Exec(`
			INSERT INTO workflow_nodes (run_id, path, kind, state, ordinal, payload)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(run_id, path) DO UPDATE SET
				kind = excluded.kind,
				state = excluded.state,
				ordinal = excluded.ordinal,
				payload = excluded.payload`,
			run.RunID, n.Path, n.Kind, n.State, n.Ordinal, n.Payload); err != nil {
			return fmt.Errorf("save workflow node %s of run %s: %w", n.Path, run.RunID, err)
		}
	}
	for _, in := range cp.Instances {
		if _, err := tx.Exec(`
			INSERT INTO workflow_instances (run_id, path, owner, body_path, item, name, ordinal, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(run_id, path) DO UPDATE SET
				owner = excluded.owner,
				body_path = excluded.body_path,
				item = excluded.item,
				name = excluded.name,
				ordinal = excluded.ordinal,
				payload = excluded.payload`,
			run.RunID, in.Path, in.Owner, in.BodyPath, in.Item, in.Name, in.Ordinal, in.Payload); err != nil {
			return fmt.Errorf("save workflow instance %s of run %s: %w", in.Path, run.RunID, err)
		}
	}
	for _, a := range cp.Attempts {
		if _, err := tx.Exec(`
			INSERT INTO workflow_attempts
				(run_id, dispatch_id, node_path, number, status, started_at, finished_at, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(run_id, dispatch_id) DO UPDATE SET
				node_path = excluded.node_path,
				number = excluded.number,
				status = excluded.status,
				started_at = excluded.started_at,
				finished_at = excluded.finished_at,
				payload = excluded.payload`,
			run.RunID, a.DispatchID, a.NodePath, a.Number, a.Status, a.StartedAt, a.FinishedAt, a.Payload); err != nil {
			return fmt.Errorf("save workflow attempt %s of run %s: %w", a.DispatchID, run.RunID, err)
		}
	}
	for _, s := range cp.Settlements {
		if _, err := tx.Exec(`
			INSERT INTO workflow_settlements
				(run_id, node_path, proposal_id, producer, action, status, recorded_at, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(run_id, node_path, proposal_id) DO UPDATE SET
				producer = excluded.producer,
				action = excluded.action,
				status = excluded.status,
				recorded_at = excluded.recorded_at,
				payload = excluded.payload`,
			run.RunID, s.NodePath, s.ProposalID, s.Producer, s.Action, s.Status, s.RecordedAt, s.Payload); err != nil {
			if isUniqueViolation(err) {
				// The partial unique index on settled rows: this run already has a
				// successful disposition for that proposal.
				return fmt.Errorf("settle workflow proposal %s of run %s: %w (already settled)", s.ProposalID, run.RunID, err)
			}
			return fmt.Errorf("save workflow settlement %s of run %s: %w", s.ProposalID, run.RunID, err)
		}
	}
	for _, e := range cp.Events {
		if _, err := tx.Exec(`
			INSERT INTO workflow_events (run_id, seq, line) VALUES (?, ?, ?)
			ON CONFLICT(run_id, seq) DO UPDATE SET line = excluded.line`,
			run.RunID, e.Seq, e.Line); err != nil {
			return fmt.Errorf("save workflow event %d of run %s: %w", e.Seq, run.RunID, err)
		}
	}
	for _, t := range cp.Trace {
		if _, err := tx.Exec(`
			INSERT INTO workflow_trace (run_id, seq, event_seq, kind, node_path, attempt, status, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(run_id, seq) DO UPDATE SET
				event_seq = excluded.event_seq,
				kind = excluded.kind,
				node_path = excluded.node_path,
				attempt = excluded.attempt,
				status = excluded.status,
				payload = excluded.payload`,
			run.RunID, t.Seq, t.EventSeq, t.Kind, t.NodePath, t.Attempt, t.Status, t.Payload); err != nil {
			return fmt.Errorf("save workflow trace record %d of run %s: %w", t.Seq, run.RunID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit workflow checkpoint %s: %w", run.RunID, err)
	}
	return nil
}

// LoadWorkflowCheckpoint reads one run's projection back. A run id with no row
// returns an error wrapping ErrWorkflowRunNotFound.
func (d *DB) LoadWorkflowCheckpoint(runID string) (WorkflowCheckpoint, error) {
	var cp WorkflowCheckpoint
	run := &cp.Run
	err := d.sql.QueryRow(`
		SELECT run_id, spec_hash, spec_path, graph_name, status, reason, dispatches, peak_in_flight,
		       started_at, finished_at, params, ledger, warnings, seq, updated_at
		FROM workflow_runs WHERE run_id = ?`, runID).Scan(
		&run.RunID, &run.SpecHash, &run.SpecPath, &run.GraphName, &run.Status, &run.Reason,
		&run.Dispatches, &run.PeakInFlight, &run.StartedAt, &run.FinishedAt,
		&run.Params, &run.Ledger, &run.Warnings, &run.Seq, &run.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkflowCheckpoint{}, fmt.Errorf("%w: %s", ErrWorkflowRunNotFound, runID)
	}
	if err != nil {
		return WorkflowCheckpoint{}, fmt.Errorf("load workflow run %s: %w", runID, err)
	}
	if cp.Nodes, err = d.workflowNodes(runID); err != nil {
		return WorkflowCheckpoint{}, err
	}
	if cp.Instances, err = d.workflowInstances(runID); err != nil {
		return WorkflowCheckpoint{}, err
	}
	if cp.Attempts, err = d.workflowAttempts(runID); err != nil {
		return WorkflowCheckpoint{}, err
	}
	if cp.Settlements, err = d.workflowSettlements(runID); err != nil {
		return WorkflowCheckpoint{}, err
	}
	if cp.Events, err = d.workflowEvents(runID); err != nil {
		return WorkflowCheckpoint{}, err
	}
	if cp.Trace, err = d.workflowTrace(runID); err != nil {
		return WorkflowCheckpoint{}, err
	}
	return cp, nil
}

func (d *DB) workflowNodes(runID string) ([]WorkflowNodeRow, error) {
	rows, err := d.sql.Query(`
		SELECT path, kind, state, ordinal, payload FROM workflow_nodes
		WHERE run_id = ? ORDER BY ordinal, path`, runID)
	if err != nil {
		return nil, fmt.Errorf("list workflow nodes of %s: %w", runID, err)
	}
	defer rows.Close()
	var out []WorkflowNodeRow
	for rows.Next() {
		var n WorkflowNodeRow
		if err := rows.Scan(&n.Path, &n.Kind, &n.State, &n.Ordinal, &n.Payload); err != nil {
			return nil, fmt.Errorf("scan workflow node: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (d *DB) workflowInstances(runID string) ([]WorkflowInstanceRow, error) {
	rows, err := d.sql.Query(`
		SELECT path, owner, body_path, item, name, ordinal, payload FROM workflow_instances
		WHERE run_id = ? ORDER BY ordinal, path`, runID)
	if err != nil {
		return nil, fmt.Errorf("list workflow instances of %s: %w", runID, err)
	}
	defer rows.Close()
	var out []WorkflowInstanceRow
	for rows.Next() {
		var in WorkflowInstanceRow
		if err := rows.Scan(&in.Path, &in.Owner, &in.BodyPath, &in.Item, &in.Name, &in.Ordinal, &in.Payload); err != nil {
			return nil, fmt.Errorf("scan workflow instance: %w", err)
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

func (d *DB) workflowAttempts(runID string) ([]WorkflowAttemptRow, error) {
	rows, err := d.sql.Query(`
		SELECT dispatch_id, node_path, number, status, started_at, finished_at, payload
		FROM workflow_attempts WHERE run_id = ? ORDER BY node_path, number`, runID)
	if err != nil {
		return nil, fmt.Errorf("list workflow attempts of %s: %w", runID, err)
	}
	defer rows.Close()
	var out []WorkflowAttemptRow
	for rows.Next() {
		var a WorkflowAttemptRow
		if err := rows.Scan(&a.DispatchID, &a.NodePath, &a.Number, &a.Status, &a.StartedAt, &a.FinishedAt, &a.Payload); err != nil {
			return nil, fmt.Errorf("scan workflow attempt: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (d *DB) workflowSettlements(runID string) ([]WorkflowSettlementRow, error) {
	rows, err := d.sql.Query(`
		SELECT node_path, proposal_id, producer, action, status, recorded_at, payload
		FROM workflow_settlements WHERE run_id = ? ORDER BY node_path, proposal_id`, runID)
	if err != nil {
		return nil, fmt.Errorf("list workflow settlements of %s: %w", runID, err)
	}
	defer rows.Close()
	var out []WorkflowSettlementRow
	for rows.Next() {
		var s WorkflowSettlementRow
		if err := rows.Scan(&s.NodePath, &s.ProposalID, &s.Producer, &s.Action, &s.Status, &s.RecordedAt, &s.Payload); err != nil {
			return nil, fmt.Errorf("scan workflow settlement: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) workflowEvents(runID string) ([]WorkflowEventRow, error) {
	rows, err := d.sql.Query(`
		SELECT seq, line FROM workflow_events WHERE run_id = ? ORDER BY seq`, runID)
	if err != nil {
		return nil, fmt.Errorf("list workflow events of %s: %w", runID, err)
	}
	defer rows.Close()
	var out []WorkflowEventRow
	for rows.Next() {
		var e WorkflowEventRow
		if err := rows.Scan(&e.Seq, &e.Line); err != nil {
			return nil, fmt.Errorf("scan workflow event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// workflowTrace reads a run's trace records in the run's own sequence order, so
// a reader reconstructs the timeline without the scheduler and without the model
// transcript.
func (d *DB) workflowTrace(runID string) ([]WorkflowTraceRow, error) {
	rows, err := d.sql.Query(`
		SELECT seq, event_seq, kind, node_path, attempt, status, payload
		FROM workflow_trace WHERE run_id = ? ORDER BY seq`, runID)
	if err != nil {
		return nil, fmt.Errorf("list workflow trace of %s: %w", runID, err)
	}
	defer rows.Close()
	var out []WorkflowTraceRow
	for rows.Next() {
		var t WorkflowTraceRow
		if err := rows.Scan(&t.Seq, &t.EventSeq, &t.Kind, &t.NodePath, &t.Attempt, &t.Status, &t.Payload); err != nil {
			return nil, fmt.Errorf("scan workflow trace record: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListWorkflowRuns returns the most recently updated runs, newest first. It is
// the listing a `yaah workflow runs` surface would print, and the way a caller
// finds the id it wants to resume.
func (d *DB) ListWorkflowRuns(limit int) ([]WorkflowRunRow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.sql.Query(`
		SELECT run_id, spec_hash, spec_path, graph_name, status, reason, dispatches, peak_in_flight,
		       started_at, finished_at, params, ledger, warnings, seq, updated_at
		FROM workflow_runs ORDER BY updated_at DESC, run_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list workflow runs: %w", err)
	}
	defer rows.Close()
	var out []WorkflowRunRow
	for rows.Next() {
		var r WorkflowRunRow
		if err := rows.Scan(&r.RunID, &r.SpecHash, &r.SpecPath, &r.GraphName, &r.Status, &r.Reason,
			&r.Dispatches, &r.PeakInFlight, &r.StartedAt, &r.FinishedAt,
			&r.Params, &r.Ledger, &r.Warnings, &r.Seq, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan workflow run: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteWorkflowCheckpoint removes one run's state. It is the cleanup path for a
// run a caller is done with — never the resume path, which must read the record
// to know what not to re-do.
func (d *DB) DeleteWorkflowCheckpoint(runID string) error {
	tx, err := d.sql.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin workflow delete %s: %w", runID, err)
	}
	defer tx.Rollback()
	for _, table := range []string{
		"workflow_nodes", "workflow_instances", "workflow_attempts", "workflow_settlements", "workflow_events", "workflow_trace",
	} {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE run_id = ?`, runID); err != nil {
			return fmt.Errorf("delete %s of run %s: %w", table, runID, err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM workflow_runs WHERE run_id = ?`, runID); err != nil {
		return fmt.Errorf("delete workflow run %s: %w", runID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit workflow delete %s: %w", runID, err)
	}
	return nil
}

// orEmptyJSON keeps a NOT NULL column honest without making the caller spell an
// empty object; the adapter always supplies one, and this is the safety net for a
// hand-built row in a test.
func orEmptyJSON(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// isUniqueViolation reports whether err is SQLite's constraint failure. The
// driver does not export typed errors, so the message is the only signal — and a
// store that silently accepted a second settled disposition would defeat the
// point of having the index at all.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "constraint failed")
}
