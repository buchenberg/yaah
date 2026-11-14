package exec

import (
	"context"
	"time"
)

// FakeRuntime is the deterministic Runtime the scheduler is tested on, and the
// reference for what a real adapter must provide. Outcomes are scripted per
// node path, the clock is virtual, and nothing sleeps: no method blocks, so a
// test that drives a Run through Advance sees exactly the sequence the script
// describes.
//
// The virtual clock is the part that makes wall-clock budgets testable. It
// advances by a scripted attempt's duration_ms at the moment that attempt's
// completion is *read back* — which the scheduler does from its own
// single-threaded commit loop — and by the delay of a retry backoff when
// Backoff is called. Neither depends on goroutine timing, so elapsed time is a
// deterministic function of the script.
//
// A FakeRuntime is not safe for concurrent use, and does not need to be: the
// scheduler is the only caller, and it calls from one goroutine.
type FakeRuntime struct {
	// Script maps a node path to the completions its attempts return, in
	// attempt order. A path with no entry answers with Default.
	Script map[string][]Completion
	// Default answers attempts whose path has no scripted entries left. When
	// a plain report is wanted, leave it zero — a zero Completion is not a
	// completion, so use NewReport for the convenience.
	Default Completion
	// HasDefault marks Default as meaningful. Without it an exhausted script
	// returns a completed report with the attempt's duration.
	HasDefault bool
	// StartErr scripts a door malfunction: a node path listed here fails to
	// start, and the run records it as a runtime error with no envelope.
	StartErr map[string]error
	// CancelErr scripts a Runtime.Cancel error per dispatch id, so the
	// cancel-error path on NodeState is reachable in a test.
	CancelErr map[string]error

	start    time.Time
	now      time.Time
	consumed map[string]int
	solved   map[string]Completion
	read     map[string]bool
	started  []Dispatch
	cancels  []string
	backoffs []time.Duration
	blocks   int
	inflight int
	peak     int
}

// NewFakeRuntime returns a fake whose virtual clock starts at t.
func NewFakeRuntime(t time.Time) *FakeRuntime {
	return &FakeRuntime{
		start:    t,
		now:      t,
		consumed: map[string]int{},
		solved:   map[string]Completion{},
		read:     map[string]bool{},
	}
}

// Report is the completion a successful report-lane dispatch returns, with the
// result text the node would deliver.
func Report(text string, d time.Duration) Completion {
	return Completion{
		Status:     StatusReport,
		Result:     text,
		DurationMs: d.Milliseconds(),
		StopReason: "completed",
	}
}

// Proposal is the completion a successful proposal-lane dispatch returns: the
// lane sealed a proposal and left it open, which is what the settlement verbs
// (yaah-0u0v.13) will act on.
func Proposal(proposalID string, d time.Duration) Completion {
	return Completion{
		Status:     StatusProposal,
		ProposalID: proposalID,
		DurationMs: d.Milliseconds(),
		StopReason: "completed",
	}
}

// Failure is a status=error completion whose envelope asserts retryability —
// the shape dispatch.RecoveryFor produces for a provider error or a timeout.
func Failure(stopReason, cause string, d time.Duration) Completion {
	return Completion{
		Status:     StatusError,
		Error:      cause,
		DurationMs: d.Milliseconds(),
		StopReason: stopReason,
		Recovery:   &Recovery{Retryable: true},
	}
}

// TerminalFailure is a status=error completion whose envelope asserts that
// re-dispatching is worthless: the retry rule must not fire on it.
func TerminalFailure(stopReason, cause string, d time.Duration) Completion {
	return Completion{
		Status:     StatusError,
		Error:      cause,
		DurationMs: d.Milliseconds(),
		StopReason: stopReason,
		Recovery:   &Recovery{Retryable: false},
	}
}

func (f *FakeRuntime) Start(_ context.Context, d Dispatch) (Handle, error) {
	if d.Node == nil || d.DispatchID == "" {
		return Handle{}, context.DeadlineExceeded
	}
	if err := f.StartErr[d.Node.Path]; err != nil {
		return Handle{}, err
	}
	f.started = append(f.started, d)
	f.inflight++
	if f.inflight > f.peak {
		f.peak = f.inflight
	}
	c := f.outcome(d.Node.Path)
	if c.DispatchID == "" {
		c.DispatchID = d.DispatchID
	}
	if c.Role == "" {
		c.Role = d.Node.Role
	}
	if c.Lane == "" {
		c.Lane = string(d.Node.Lane)
	}
	f.solved[d.DispatchID] = c
	return Handle{DispatchID: d.DispatchID}, nil
}

// Completed reports the scripted outcome once it has been read. The answer is
// stable: a re-poll returns the same completion and charges the clock only the
// first time, which is what lets the scheduler poll the head of its dispatch
// order until that attempt's turn to commit arrives.
func (f *FakeRuntime) Completed(h Handle) (Completion, bool) {
	c, ok := f.solved[h.DispatchID]
	if !ok {
		return Completion{}, false
	}
	if !f.read[h.DispatchID] {
		f.read[h.DispatchID] = true
		f.inflight--
		f.now = f.now.Add(c.Duration())
	}
	return c, true
}

func (f *FakeRuntime) Block(_ context.Context) error {
	f.blocks++
	return nil
}

func (f *FakeRuntime) Cancel(h Handle) error {
	f.cancels = append(f.cancels, h.DispatchID)
	if err := f.CancelErr[h.DispatchID]; err != nil {
		return err
	}
	if _, ok := f.solved[h.DispatchID]; !ok {
		return nil
	}
	if !f.read[h.DispatchID] {
		f.read[h.DispatchID] = true
		f.inflight--
	}
	// A cancelled attempt reports the envelope a real lane would return once
	// the cancellation landed, so a late completion is observable in a test.
	// retryable is false: the run deliberately stopped this attempt, and a
	// completion that invited retry would contradict the cancellation.
	f.solved[h.DispatchID] = Completion{
		Status:     StatusError,
		DispatchID: h.DispatchID,
		StopReason: "canceled",
		Recovery:   &Recovery{Retryable: false},
	}
	return nil
}

// Backoff advances the virtual clock by d and returns immediately. Tests see
// the delay in the clock and in Backoffs; they never sleep.
func (f *FakeRuntime) Backoff(_ context.Context, d time.Duration) error {
	f.backoffs = append(f.backoffs, d)
	f.now = f.now.Add(d)
	return nil
}

func (f *FakeRuntime) Now() time.Time { return f.now }

// Advance moves the virtual clock, which is how a test makes a wall-clock
// budget expire between two scheduler cycles.
func (f *FakeRuntime) Advance(d time.Duration) { f.now = f.now.Add(d) }

// SetNow places the virtual clock absolutely.
func (f *FakeRuntime) SetNow(t time.Time) { f.now = t }

// Peak reports the highest number of attempts the fake held in flight at once,
// which is the door-side view of the concurrency cap.
func (f *FakeRuntime) Peak() int { return f.peak }

// InFlight reports the attempts started and not yet read back or cancelled.
func (f *FakeRuntime) InFlight() int { return f.inflight }

// Started returns the dispatch requests in start order.
func (f *FakeRuntime) Started() []Dispatch {
	out := make([]Dispatch, len(f.started))
	copy(out, f.started)
	return out
}

// Cancels returns the dispatch ids the run asked to cancel, in request order.
func (f *FakeRuntime) Cancels() []string {
	out := make([]string, len(f.cancels))
	copy(out, f.cancels)
	return out
}

// Backoffs returns the retry delays the run honored, in order.
func (f *FakeRuntime) Backoffs() []time.Duration {
	out := make([]time.Duration, len(f.backoffs))
	copy(out, f.backoffs)
	return out
}

// Blocks counts the idle requests the scheduler made. A run that finished
// without ever calling Block did all its work in dispatch order.
func (f *FakeRuntime) Blocks() int { return f.blocks }

func (f *FakeRuntime) outcome(path string) Completion {
	n := f.consumed[path]
	f.consumed[path] = n + 1
	if scripted := f.Script[path]; n < len(scripted) {
		return scripted[n]
	}
	if f.HasDefault {
		return f.Default
	}
	return Report("", 0)
}
