// Package trace defines the typed record a workflow run's trace surface reads
// (yaah-0u0v.17).
//
// A record is not a second state machine. It is a PROJECTION of one reducer
// event: internal/workflow/exec derives the records for an event from the event
// itself plus the frozen facts the graph already carries (node id, node kind,
// declared lane, the container a fan-out instance belongs to), at the same
// place it appends the event's canonical log line. So the event log and the
// trace are two renderings of one sequence — the log is the byte-identical
// artifact the determinism test compares, and a record is the typed view a
// reader can query without parsing that text back.
//
// This package is deliberately a leaf: it imports nothing but the standard
// library, so exec can project records without giving this package (or its
// readers) a dependency on the scheduler. The vocabulary is therefore closed
// here, and `internal/workflow/exec` imports it — never the other way round.
//
// # What a record carries
//
// The bead's field list — run id, spec hash, node id, attempt, kind, status,
// scope id, lane, proposal id, action, result, output digest — is one struct
// with every field optional, because which facts exist depends on the record
// kind: a settle record states an action, a dispatch record states an attempt
// and a result, and a run record states neither. Empty means "this record kind
// does not carry that fact", never "unknown".
//
// Nothing here is a clock. A record holds no timestamp, deliberately: the
// sequence numbers order the trace, and a wall-clock field would make two runs
// of one spec over one scripted set of completions produce different bytes.
package trace

import (
	"crypto/sha256"
	"encoding/hex"
)

// Kind names one trace record. The vocabulary is the bead's seven words, and
// each one is derived from exactly one reducer event (see the derivation table
// in internal/workflow/exec/trace.go).
type Kind string

// Record kinds.
const (
	// KindRunStarted opens a run's trace. A fresh run records it from
	// run_started; a run a process picked up from its durable checkpoint
	// records the same kind with StatusResumed, because "this process entered
	// execution here" is the fact a reader needs and the vocabulary has one
	// word for it.
	KindRunStarted Kind = "workflow.started"
	// KindNodeStarted records that a node became ready — the moment any node,
	// whether it dispatches (delegate), dispatches nothing (emit, settle,
	// fan-out, branch, loop, aggregate), or is decided by a gate, starts.
	KindNodeStarted Kind = "node.started"
	// KindNodeFinished records a node's terminal disposition and the content
	// address of the output it recorded.
	KindNodeFinished Kind = "node.finished"
	// KindDispatch records one attempt's trip through the run's door: a record
	// with StatusStarted when the attempt was handed over, and a second record
	// carrying the envelope's own status when it came back. Both name the same
	// DispatchID, so the pair joins.
	KindDispatch Kind = "dispatch"
	// KindProposal records that a proposal-lane attempt sealed a proposal: the
	// handle the run keeps open until a settle node disposes of it.
	KindProposal Kind = "proposal"
	// KindSettle records one settlement: the disposition the run asked the door
	// for (StatusAsked) and the disposition the door reported (the door's own
	// status, verbatim: settled, conflict, or error).
	KindSettle Kind = "settle"
	// KindRunEnded closes a run's trace with its disposition and stopping
	// cause.
	KindRunEnded Kind = "workflow.ended"
)

// Kinds lists the record vocabulary in lifecycle order. It exists so a reader
// (and the exhaustive test) can enumerate what the surface may emit instead of
// keeping a second hand-maintained list.
func Kinds() []Kind {
	return []Kind{
		KindRunStarted,
		KindNodeStarted,
		KindDispatch,
		KindProposal,
		KindSettle,
		KindNodeFinished,
		KindRunEnded,
	}
}

// Status values a record states. The set is small on purpose: a record's
// status is either one of the lifecycle words the scheduler already owns, one
// of the two door-relative words below, or a status the door reported verbatim
// (a dispatch envelope's proposal/report/shared/completed/error, a settlement's
// settled/conflict/error). Nothing here invents a word for the world's answers.
const (
	// StatusStarted marks a fact that has begun: an attempt handed to the
	// door, a node that became ready.
	StatusStarted = "started"
	// StatusResumed marks the workflow.started record a resumed run writes.
	StatusResumed = "resumed"
	// StatusAsked marks the settle record written BEFORE the door is called,
	// so a run that stopped with a disposition in flight has a trace line
	// saying the ask happened even though no answer was recorded.
	StatusAsked = "asked"
	// StatusRuntimeError marks a dispatch the door itself malfunctioned on.
	// There is no envelope behind such an event, so the record cannot quote a
	// status the world never sent.
	StatusRuntimeError = "runtime_error"

	// Lifecycle statuses, matching exec's node states so a trace line and a
	// node's state read the same word.
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusSkipped   = "skipped"
	StatusCancelled = "cancelled"
)

// Record is one projected trace fact. Every field is optional, and a field is
// empty exactly when the record kind has nothing to say about it.
type Record struct {
	// Seq is the record's own 1-based ordinal within the run. It is the
	// primary key of the run's trace and the order a reader renders.
	Seq int `json:"seq"`
	// EventSeq is the canonical event log line this record projects, so a trace
	// line and the log line it renders from always join.
	EventSeq int `json:"event_seq,omitempty"`
	// Kind is the record kind.
	Kind Kind `json:"kind"`
	// RunID is the run the record belongs to, stamped on every record so a
	// record is meaningful on its own.
	RunID string `json:"run_id"`
	// SpecHash is the content address of the document the run was started
	// from (spec.SpecHash): the same digest a resume compares, so a trace
	// names exactly which document produced these facts.
	SpecHash string `json:"spec_hash,omitempty"`

	// NodePath, NodeID, and NodeKind identify the node a record is about. A
	// fan-out instance's path is its instance path, which is the identity the
	// scheduler and the checkpoint both use.
	NodePath string `json:"node_path,omitempty"`
	NodeID   string `json:"node_id,omitempty"`
	NodeKind string `json:"node_kind,omitempty"`
	// ScopeID is the container whose item scope the node runs in: the fan-out
	// or loop node path for a body instance, empty for a top-level node. It is
	// how a reader tells two instances of one body apart.
	ScopeID string `json:"scope_id,omitempty"`
	// Lane is the dispatch lane the graph declared (report or proposal), or the
	// lane the envelope reported, verbatim.
	Lane string `json:"lane,omitempty"`
	// Attempt is the 1-based attempt ordinal, for records about a dispatch.
	Attempt int `json:"attempt,omitempty"`
	// DispatchID is "<node path>#<attempt>", the identity the dispatch envelope
	// already reports, so a trace joins the request and the completion.
	DispatchID string `json:"dispatch_id,omitempty"`

	// Status is the record's disposition: a lifecycle word, a door-relative
	// word (started/asked), or the door's own status verbatim.
	Status string `json:"status,omitempty"`
	// Reason names why, when the fact has a cause: a failed node's reason, a
	// settlement's error, a run's stopping cause.
	Reason string `json:"reason,omitempty"`

	// ProposalID is the sealed proposal a dispatch or settlement record is
	// about.
	ProposalID string `json:"proposal_id,omitempty"`
	// Action is the settlement verb the run asked for (select, apply, release,
	// discard).
	Action string `json:"action,omitempty"`

	// Result is the recorded agent output — the delivered report text or the
	// sealed proposal's result body — so replay needs no model transcript.
	Result string `json:"result,omitempty"`
	// OutputDigest is the SHA-256 of the recorded output (see Digest). It is
	// what lets a replay assert that the output it re-derived is the output the
	// run recorded, without comparing two copies of the text.
	OutputDigest string `json:"output_digest,omitempty"`
}

// Digest is the content address of a recorded output: the SHA-256 hex digest of
// its bytes. An empty output has no digest rather than the digest of nothing,
// so "recorded an empty result" and "recorded no result" stay distinguishable.
func Digest(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
