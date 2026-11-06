// Package runs defines the protocol-neutral run record behind yaah's
// task-style surfaces: a unit of work a caller starts and can poll until it
// settles. It is the shared shape the MCP serve concurrency work (bead
// yaah-okj0.1, Phase A) and the A2A adapter (Phase B) will both read.
//
// The neutrality is the point, and it is what keeps the two protocols
// honest:
//
//   - No MCP types and no A2A types live here. Nothing in this package knows
//     about progressToken, JSON-RPC ids, task objects, or artifact parts.
//     Adapters encode a Run into their own wire shape at the edge; the record
//     stays neutral.
//   - State is THE lifecycle enum. Both tasks/get surfaces read it and
//     neither owns it, so the two can never disagree about whether a run is
//     live or finished.
//   - Pending keys are stable for the run's lifetime, because both protocols
//     dedupe across polls on them: the same open request must come back with
//     the same key from minting to answer.
//   - The record belongs in the memory DB (internal/memory) next to sessions,
//     not in a process map, so a run that was live when the process died is
//     reported failed/interrupted rather than working forever.
//
// Persistence is deliberately NOT implemented here: this package is the value
// type and the invariants those constraints are reviewed against (see
// docs/architecture.md, "Run record"). It imports only the standard library.
package runs

import (
	"slices"
	"time"
)

// Run is one callable unit of work: a server-minted id, the yaah session it
// runs in, one lifecycle state, and the terminal output once it settles.
//
// A Run is a value, not a live object. Surfaces copy it out of the store and
// read it; none of them owns the state. State is the single source of truth —
// Result, Err, and Pending are content derived from it, never a second
// lifecycle.
type Run struct {
	// RunID is server-minted, opaque, and stable across a process restart.
	// Callers treat it as an unparsed token; an adapter maps it to its own id
	// space (an MCP task id, an A2A task id) without rewriting it.
	RunID string

	// SessionID is the yaah conversation id (a session in the memory DB)
	// that the run belongs to. Several runs may share one session; the run
	// id, not the session id, identifies the work.
	SessionID string

	// State is the single source of truth for lifecycle. See State.
	State State

	// Result is the terminal text of a completed run, and empty while the run
	// is live. It is not the incremental token stream — a caller that wants
	// progress reads Seq.
	Result string

	// Err is the terminal error message of a failed run, and empty otherwise.
	// It is a message, not an error value: the record crosses process and
	// protocol boundaries, where a Go error cannot travel.
	Err string

	// Pending holds the input_required requests awaiting a caller's answer.
	// Their keys are unique for the run's lifetime — see Pending.
	Pending []Pending

	// Seq is a monotonic per-run event cursor: a caller passes back the last
	// value it saw to replay the events it missed. It never decreases.
	Seq uint64

	// CreatedAt is when the server minted the run; UpdatedAt is when the
	// record last changed. Neither is a lifecycle signal — State is.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Terminal reports whether the run has reached a state it can never leave:
// completed, failed, or cancelled. A terminal run still answers reads (its
// result or error), but it accepts no new input; a live run — queued,
// working, or input_required — does.
//
// An unrecognised state reports false: progress, not settlement, is the safe
// reading of a state this package does not know.
func (r Run) Terminal() bool {
	switch r.State {
	case StateCompleted, StateFailed, StateCancelled:
		return true
	}
	return false
}

// PendingByKey returns the pending request carrying key, and whether one was
// found. Both protocol adapters resolve an answer this way, which is what
// makes deduplication across polls possible: a request that is still open
// comes back with the same key, so answering it twice is one answer rather
// than two requests.
func (r Run) PendingByKey(key string) (Pending, bool) {
	for _, p := range r.Pending {
		if p.Key == key {
			return p, true
		}
	}
	return Pending{}, false
}

// DuplicatePendingKeys reports the keys that appear more than once in
// Pending, in first-duplicate order. The record requires unique keys for the
// run's lifetime; a non-empty result is a bug in whatever appended the
// request, and an adapter that mints request ids from keys must refuse the
// record rather than pick one of the duplicates.
func (r Run) DuplicatePendingKeys() []string {
	var dups []string
	seen := make(map[string]struct{}, len(r.Pending))
	for _, p := range r.Pending {
		if _, ok := seen[p.Key]; ok {
			if !slices.Contains(dups, p.Key) {
				dups = append(dups, p.Key)
			}
			continue
		}
		seen[p.Key] = struct{}{}
	}
	return dups
}

// Pending is one input_required request: something the run needs from the
// caller before it can continue.
type Pending struct {
	// Key is stable and unique for the run's lifetime. An adapter maps it to
	// its own request id (an MCP inputRequests key, an A2A input-required
	// id), and both protocols dedupe across polls on it, so the same open
	// request must keep the same key from minting to answer. Re-minting a key
	// for the same question reads as a second question.
	Key string

	// Prompt is the question put to the caller, as neutral text. Rendering,
	// choices, and answer schemas belong to the adapter, not the record.
	Prompt string
}
