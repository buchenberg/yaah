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

	DefaultDigestWindow = 1500 * time.Millisecond
	DefaultMaxWatches   = 32
	DefaultWatchExpiry  = 24 * time.Hour
	MaxWatchExpiry      = 7 * 24 * time.Hour
)

// WatchRecord is the durable half of an armed watch. The manager owns the
// semantics; a Store only persists and returns records.
type WatchRecord struct {
	ID         string
	On         string
	Until      string
	State      string
	Evidence   string
	ArmedAt    int64 // unix nanoseconds
	ExpiresAt  int64 // unix nanoseconds; 0 means no expiry
	FiredAt    int64 // unix nanoseconds; 0 until fired
	IntervalMs int64
}

// Store persists watches so they survive a session restart. Implementations
// must be safe for concurrent use and must treat an unknown id as a no-op on
// delete.
type Store interface {
	SaveWatch(sessionID string, rec WatchRecord) error
	ListWatches(sessionID string) ([]WatchRecord, error)
	DeleteWatch(sessionID, id string) error
}

// Options configures a Manager. Zero values take the defaults above.
type Options struct {
	SessionID     string
	Deps          Deps
	Store         Store
	OnWake        func(string)
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
	Fired       bool   `json:"fired"`
	Source      string `json:"source,omitempty"`
	On          string `json:"on"`
	Evidence    string `json:"evidence,omitempty"`
	ElapsedMs   int64  `json:"elapsed_ms"`
	TimedOut    bool   `json:"timed_out,omitempty"`
	Interrupted bool   `json:"interrupted,omitempty"`
	Handle      string `json:"handle,omitempty"`
	Notes       string `json:"notes,omitempty"`
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
	ID         string `json:"id"`
	On         string `json:"on"`
	Until      string `json:"until,omitempty"`
	State      string `json:"state"`
	Evidence   string `json:"evidence,omitempty"`
	ArmedAt    int64  `json:"armed_at"`
	ExpiresAt  int64  `json:"expires_at,omitempty"`
	IntervalMs int64  `json:"interval_ms,omitempty"`
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

	onWake func(string)

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
	wg        sync.WaitGroup

	mu        sync.Mutex
	watches   map[string]*armedWatch
	nextID    int
	pending   []firedWatch
	timer     *time.Timer
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

	state    string
	evidence string
	firedAt  time.Time
}

type firedWatch struct {
	id       string
	spec     Spec
	evidence string
	missed   bool
	// w is the live record the fire came from. Matching it on flush keeps a
	// fired watch from deleting a same-id replacement armed after it.
	w *armedWatch
}
