package runs

import "fmt"

// State is the lifecycle of a run. It is THE enum: the MCP serve surface
// (tasks/get) and the A2A adapter both read it, and neither keeps a second
// copy. Adapters translate State into their own wire spelling at the edge;
// the record never stores a protocol state, so the two protocols cannot
// disagree about whether a run is live.
type State int

// Run states. The zero value is StateQueued: a record that was minted but has
// not started working yet.
//
// StateInputRequired means the run is waiting on a caller's answer to at least
// one Pending request; it is a live state, not a terminal one. The three
// terminal states — completed, failed, cancelled — are the only ones a run can
// never leave.
const (
	StateQueued State = iota
	StateWorking
	StateInputRequired
	StateCompleted
	StateFailed
	StateCancelled
)

// String returns the stable name of the state: "queued", "working",
// "input_required", "completed", "failed", or "cancelled". The spelling is
// part of the record's contract (logs, traces, and tests compare it); a value
// outside the enum renders as "state(N)" so a corrupt or future state is
// visible rather than silently reported as queued.
func (s State) String() string {
	switch s {
	case StateQueued:
		return "queued"
	case StateWorking:
		return "working"
	case StateInputRequired:
		return "input_required"
	case StateCompleted:
		return "completed"
	case StateFailed:
		return "failed"
	case StateCancelled:
		return "cancelled"
	}
	return fmt.Sprintf("state(%d)", int(s))
}
