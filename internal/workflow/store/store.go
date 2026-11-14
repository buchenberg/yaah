// Package store is the SQLite adapter for durable workflow run state
// (yaah-0u0v.16).
//
// It exists to keep two packages apart that have no business knowing each other:
// internal/workflow/exec owns the run state and must not depend on a database,
// and internal/memory owns the session store family and must not depend on what a
// fan-out item is. So exec declares the port (its Store interface) and this
// package — the only one that imports both — translates a checkpoint into rows
// and back.
//
// # What is projected where
//
// One checkpoint becomes: the run row, one row per node (the serialized
// NodeState, plus the state column so a query can ask "what had terminated"
// without decoding), one row per body instance, one row per attempt (derived from
// the node states — the attempt history is not duplicated as separate truth), one
// row per settlement disposition, the new lines of the event log, and the trace
// records projected from those same events (yaah-0u0v.17). Nothing here decides
// anything: every fact comes from the reducer's snapshot.
//
// # Idempotence
//
// The reducer re-writes the WHOLE projection on every event, so every write is an
// upsert keyed by the run and the record's own identity. A replayed checkpoint is
// therefore a no-op rather than a duplicate, which is what lets a resume and an
// uninterrupted run produce the same rows.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/workflow/exec"
	"github.com/buchenberg/yaah/internal/workflow/trace"
)

// Store adapts *memory.DB to exec.Store.
type Store struct {
	db *memory.DB
}

var _ exec.Store = (*Store)(nil)

// New wraps a session store. A nil db is refused rather than deferred: a Store
// that cannot write is exactly the run state the caller thinks it has.
func New(db *memory.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("workflow/store: nil database")
	}
	return &Store{db: db}, nil
}

// Close releases the underlying database. It is the caller's job to close a
// store it opened: on Windows an open SQLite file cannot be removed, so a test
// that hands the store a temp directory must close before that directory is
// torn down.
func (s *Store) Close() error { return s.db.Close() }

// SaveCheckpoint writes the run's projection. Every statement is an upsert, so
// the call is idempotent and re-saving an unchanged projection changes nothing
// but the run's updated_at.
func (s *Store) SaveCheckpoint(ctx context.Context, cp exec.Checkpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cp.RunID == "" {
		return fmt.Errorf("workflow/store: checkpoint with no run id")
	}
	params, err := jsonOr(cp.Params, "{}")
	if err != nil {
		return fmt.Errorf("workflow/store: encoding params of run %s: %w", cp.RunID, err)
	}
	ledger, err := json.Marshal(cp.Ledger)
	if err != nil {
		return fmt.Errorf("workflow/store: encoding ledger of run %s: %w", cp.RunID, err)
	}
	warnings, err := jsonOr(cp.Warnings, "[]")
	if err != nil {
		return fmt.Errorf("workflow/store: encoding warnings of run %s: %w", cp.RunID, err)
	}
	out := memory.WorkflowCheckpoint{
		Run: memory.WorkflowRunRow{
			RunID:        cp.RunID,
			SpecHash:     cp.SpecHash,
			SpecPath:     cp.SpecPath,
			GraphName:    cp.GraphName,
			Status:       string(cp.Status),
			Reason:       cp.Reason,
			Dispatches:   cp.Dispatches,
			PeakInFlight: cp.PeakInFlight,
			StartedAt:    unixNano(cp.StartedAt),
			FinishedAt:   unixNano(cp.FinishedAt),
			Params:       params,
			Ledger:       string(ledger),
			Warnings:     warnings,
			Seq:          cp.Seq,
			UpdatedAt:    time.Now().UnixNano(),
		},
	}
	for i, n := range cp.Nodes {
		if n == nil {
			continue
		}
		payload, perr := json.Marshal(n)
		if perr != nil {
			return fmt.Errorf("workflow/store: encoding node %s of run %s: %w", n.Path, cp.RunID, perr)
		}
		out.Nodes = append(out.Nodes, memory.WorkflowNodeRow{
			Path:    n.Path,
			Kind:    string(n.Kind),
			State:   string(n.State),
			Ordinal: i,
			Payload: string(payload),
		})
		for _, a := range n.Attempts {
			if a == nil {
				continue
			}
			apayload, aerr := json.Marshal(a)
			if aerr != nil {
				return fmt.Errorf("workflow/store: encoding attempt %s of run %s: %w", a.DispatchID, cp.RunID, aerr)
			}
			out.Attempts = append(out.Attempts, memory.WorkflowAttemptRow{
				DispatchID: a.DispatchID,
				NodePath:   a.NodePath,
				Number:     a.Number,
				Status:     a.Status,
				StartedAt:  unixNano(a.StartedAt),
				FinishedAt: unixNano(a.FinishedAt),
				Payload:    string(apayload),
			})
		}
		for _, st := range n.Settled {
			spayload, serr := json.Marshal(st)
			if serr != nil {
				return fmt.Errorf("workflow/store: encoding settlement of %s under %s: %w", st.ProposalID, n.Path, serr)
			}
			out.Settlements = append(out.Settlements, memory.WorkflowSettlementRow{
				NodePath:   n.Path,
				ProposalID: st.ProposalID,
				Producer:   st.Producer,
				Action:     st.Action,
				Status:     st.Status,
				RecordedAt: time.Now().UnixNano(),
				Payload:    string(spayload),
			})
		}
	}
	for i, in := range cp.Instances {
		payload, perr := json.Marshal(in)
		if perr != nil {
			return fmt.Errorf("workflow/store: encoding instance %s of run %s: %w", in.Path, cp.RunID, perr)
		}
		out.Instances = append(out.Instances, memory.WorkflowInstanceRow{
			Path:     in.Path,
			Owner:    in.Owner,
			BodyPath: in.BodyPath,
			Item:     in.Item,
			Name:     in.Name,
			Ordinal:  i,
			Payload:  string(payload),
		})
	}
	for i, line := range cp.Events {
		out.Events = append(out.Events, memory.WorkflowEventRow{
			Seq:  cp.EventBase + i + 1,
			Line: line,
		})
	}
	// The trace records are the typed projection of the same events, appended in
	// the same checkpoint transaction: a run can never record an event whose
	// trace record is missing, because neither becomes visible alone.
	for i, rec := range cp.Trace {
		if rec.Seq == 0 {
			rec.Seq = cp.TraceBase + i + 1
		}
		payload, terr := json.Marshal(rec)
		if terr != nil {
			return fmt.Errorf("workflow/store: encoding trace record %d of run %s: %w", rec.Seq, cp.RunID, terr)
		}
		out.Trace = append(out.Trace, memory.WorkflowTraceRow{
			Seq:      rec.Seq,
			EventSeq: rec.EventSeq,
			Kind:     string(rec.Kind),
			NodePath: rec.NodePath,
			Attempt:  rec.Attempt,
			Status:   rec.Status,
			Payload:  string(payload),
		})
	}
	return s.db.SaveWorkflowCheckpoint(out)
}

// LoadCheckpoint reads one run back. A run id with no record returns an error
// wrapping exec.ErrRunNotFound, so a caller can tell "wrong id" from "store
// broken".
//
// The attempt and settlement rows are not decoded here: the attempt history and
// the dispositions are already inside the node states, and the rows exist for
// querying (a trace join, a "was this proposal settled" question) rather than as a
// second copy the resume would have to reconcile.
func (s *Store) LoadCheckpoint(ctx context.Context, runID string) (exec.Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return exec.Checkpoint{}, err
	}
	rows, err := s.db.LoadWorkflowCheckpoint(runID)
	if err != nil {
		if isNotFound(err) {
			return exec.Checkpoint{}, fmt.Errorf("%w: %s", exec.ErrRunNotFound, runID)
		}
		return exec.Checkpoint{}, err
	}
	cp := exec.Checkpoint{
		RunID:        rows.Run.RunID,
		SpecHash:     rows.Run.SpecHash,
		SpecPath:     rows.Run.SpecPath,
		GraphName:    rows.Run.GraphName,
		Status:       exec.RunStatus(rows.Run.Status),
		Reason:       rows.Run.Reason,
		Dispatches:   rows.Run.Dispatches,
		PeakInFlight: rows.Run.PeakInFlight,
		StartedAt:    fromUnixNano(rows.Run.StartedAt),
		FinishedAt:   fromUnixNano(rows.Run.FinishedAt),
		Seq:          rows.Run.Seq,
	}
	if rows.Run.Ledger != "" {
		if err := json.Unmarshal([]byte(rows.Run.Ledger), &cp.Ledger); err != nil {
			return exec.Checkpoint{}, fmt.Errorf("workflow/store: decoding ledger of run %s: %w", runID, err)
		}
	}
	if rows.Run.Params != "" && rows.Run.Params != "{}" {
		if err := json.Unmarshal([]byte(rows.Run.Params), &cp.Params); err != nil {
			return exec.Checkpoint{}, fmt.Errorf("workflow/store: decoding params of run %s: %w", runID, err)
		}
	}
	if rows.Run.Warnings != "" && rows.Run.Warnings != "[]" {
		if err := json.Unmarshal([]byte(rows.Run.Warnings), &cp.Warnings); err != nil {
			return exec.Checkpoint{}, fmt.Errorf("workflow/store: decoding warnings of run %s: %w", runID, err)
		}
	}
	for _, n := range rows.Nodes {
		var st exec.NodeState
		if err := json.Unmarshal([]byte(n.Payload), &st); err != nil {
			return exec.Checkpoint{}, fmt.Errorf("workflow/store: decoding node %s of run %s: %w", n.Path, runID, err)
		}
		cp.Nodes = append(cp.Nodes, &st)
	}
	for _, in := range rows.Instances {
		var state exec.InstanceState
		if err := json.Unmarshal([]byte(in.Payload), &state); err != nil {
			return exec.Checkpoint{}, fmt.Errorf("workflow/store: decoding instance %s of run %s: %w", in.Path, runID, err)
		}
		cp.Instances = append(cp.Instances, state)
	}
	for _, e := range rows.Events {
		cp.Events = append(cp.Events, e.Line)
	}
	cp.EventBase = len(cp.Events)
	for _, t := range rows.Trace {
		var rec trace.Record
		if err := json.Unmarshal([]byte(t.Payload), &rec); err != nil {
			return exec.Checkpoint{}, fmt.Errorf("workflow/store: decoding trace record %d of run %s: %w", t.Seq, runID, err)
		}
		cp.Trace = append(cp.Trace, rec)
	}
	cp.TraceBase = len(cp.Trace)
	return cp, nil
}

func isNotFound(err error) bool {
	return errors.Is(err, memory.ErrWorkflowRunNotFound)
}

// jsonOr encodes v, or returns the fallback when v is nil, so a NOT NULL column
// never reads back as the empty string.
func jsonOr(v any, fallback string) (string, error) {
	switch t := v.(type) {
	case map[string]any:
		if t == nil {
			return fallback, nil
		}
	case []string:
		if t == nil {
			return fallback, nil
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unixNano(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func fromUnixNano(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}
