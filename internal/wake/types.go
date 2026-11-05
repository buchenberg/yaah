package wake

import (
	"context"
	"sync"
	"time"
)

// Watch lifecycle states persisted by a Store.
const (
	StateArmed = "armed"
	StateFired = "fired"
)

// Defaults and bounds. The wait bound is deliberately generous: a wait is
// turn-scoped and costs no model turns, but an unbounded tool call would
// wedge the session. Watches are bounded separately because they outlive the
// turn.
const (
	DefaultWaitTimeout = 15 * time.Minute
	MaxWaitTimeout     = 24 * time.Hour

	DefaultDigestWindow  = 1500 * time.Millisecond
	DefaultMaxWatches    = 32
	DefaultWatchExpiry   = 24 * time.Hour
	MaxWatchExpiry       = 7 * 24 * time.Hour
	DefaultLeaseDuration = 5 * time.Minute
)

// WatchRecord is the durable half of an armed watch. The manager owns the
// semantics; a Store only persists and returns records.
type WatchRecord struct {
	ID         string
	On         string
	Until      string
	State      string
	Evidence   Evidence
	ArmedAt    int64 // unix nanoseconds
	ExpiresAt  int64 // unix nanoseconds; 0 means no expiry
	FiredAt    int64 // unix nanoseconds; 0 until fired
	IntervalMs int64
	Owner      string
	LeaseUntil int64 // unix nanoseconds; informational last-claim lease
}

// Store persists watches so they survive a session restart. Implementations
// must be safe for concurrent use and must treat an unknown id as a no-op on
// delete.
type Store interface {
	SaveWatch(sessionID string, rec WatchRecord) error
	ListWatches(sessionID string) ([]WatchRecord, error)
	DeleteWatch(sessionID, id string) error
	// ClaimWatches makes owner the owner of every watch for the session and
	// returns the claimed set. Last resume wins: a later claim takes over,
	// and the previous owner's ClaimFire calls then fail.
	ClaimWatches(sessionID, owner string, leaseUntil int64) ([]WatchRecord, error)
	// ClaimFire transitions an armed watch owned by owner to fired and
	// reports whether this owner still owned it. armedAt scopes the claim
	// to the watch generation, so a same-id replacement is never marked.
	ClaimFire(sessionID, id, owner string, armedAt, firedAt, leaseUntil int64, evidence string) (bool, error)
}

// Observer receives lifecycle notifications for tracing and hook emission.
// Implementations must be cheap and must not block the poll path. The target
// of a predicate is deliberately not part of these calls: a URL, command or
// path must not leak into a hook log or span attribute.
type Observer interface {
	// WaitStarted starts observing a blocking wait and returns a context for
	// WaitFinished (the implementation may attach a span).
	WaitStarted(ctx context.Context, spec Spec, timeout time.Duration) context.Context
	WaitFinished(ctx context.Context, spec Spec, fired, timedOut, interrupted, errored bool, elapsed time.Duration)
	WatchArmed(info WatchInfo)
	WatchFired(info WatchInfo, missed bool)
	WatchCancelled(id string)
}

// Options configures a Manager. Zero values take the defaults above.
type Options struct {
	SessionID string
	Deps      Deps
	Store     Store
	OnWake    func(string)
	Observer  Observer
	// OwnerID identifies this process in the watch store. Empty uses the
	// process id. LeaseDuration is informational; the next resume claims
	// regardless (last resume wins).
	OwnerID       string
	LeaseDuration time.Duration
	DigestWindow  time.Duration
	MaxWatches    int
	DefaultExpiry time.Duration
	MaxExpiry     time.Duration
}

// WaitRequest is one blocking wait_for call.
type WaitRequest struct {
	On       string
	Until    string
	Timeout  time.Duration
	Interval time.Duration
	Handle   string
}

// Result is the structured outcome of a blocking wait. TimedOut and
// Interrupted are distinct so a caller abort is never reported as a normal
// timeout (the same distinction subagent_jobs wait keeps).
type Result struct {
	Fired       bool      `json:"fired"`
	Source      string    `json:"source,omitempty"`
	On          string    `json:"on"`
	Evidence    *Evidence `json:"evidence,omitempty"`
	ElapsedMs   int64     `json:"elapsed_ms"`
	TimedOut    bool      `json:"timed_out,omitempty"`
	Interrupted bool      `json:"interrupted,omitempty"`
	Handle      string    `json:"handle,omitempty"`
	Notes       string    `json:"notes,omitempty"`
}

// ArmRequest is one watch arm request.
type ArmRequest struct {
	On        string
	Until     string
	ID        string
	ExpiresIn time.Duration
	Interval  time.Duration
}

// WatchInfo is the serializable snapshot the watch tool reports.
type WatchInfo struct {
	ID         string    `json:"id"`
	On         string    `json:"on"`
	Until      string    `json:"until,omitempty"`
	State      string    `json:"state"`
	Evidence   *Evidence `json:"evidence,omitempty"`
	ArmedAt    int64     `json:"armed_at"`
	ExpiresAt  int64     `json:"expires_at,omitempty"`
	IntervalMs int64     `json:"interval_ms,omitempty"`
}

// Manager owns the blocking waits and the armed watches for one session.
// Safe for concurrent use.
type Manager struct {
	sessionID string
	deps      Deps
	store     Store

	digestWindow  time.Duration
	maxWatches    int
	defaultExpiry time.Duration
	maxExpiry     time.Duration

	onWake        func(string)
	observer      Observer
	ownerID       string
	leaseDuration time.Duration

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
	wg        sync.WaitGroup

	mu        sync.Mutex
	watches   map[string]*armedWatch
	nextID    int
	pending   []firedWatch
	timer     Timer
	flushDone chan struct{} // closed by the digest callback or by Close
	closed    bool
}

type armedWatch struct {
	id       string
	spec     Spec
	start    time.Time
	expires  time.Time
	interval time.Duration
	cancel   context.CancelFunc
	owner    string
	lease    time.Time

	state    string
	evidence Evidence
	firedAt  time.Time
}

type firedWatch struct {
	id       string
	spec     Spec
	evidence Evidence
	missed   bool
	// The rehydrated missed-wake report carries the row's timing fields so
	// the observer sees the same WatchInfo a live fire would.
	armedAt    int64
	expiresAt  int64
	intervalMs int64
	// w is the live record the fire came from. Matching it on flush keeps a
	// fired watch from deleting a same-id replacement armed after it.
	w *armedWatch
}
