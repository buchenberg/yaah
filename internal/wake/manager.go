package wake

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// digestCloseTimeout bounds how long Close waits for an in-flight digest
// callback. A wedged view sink must not hang teardown; the fired rows stay in
// the store and the next resume reports them as missed wakes.
const digestCloseTimeout = 5 * time.Second

// NewManager creates a session's manager. The caller must Close it.
func NewManager(opts Options) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		sessionID:     opts.SessionID,
		deps:          opts.Deps,
		store:         opts.Store,
		digestWindow:  opts.DigestWindow,
		maxWatches:    opts.MaxWatches,
		defaultExpiry: opts.DefaultExpiry,
		maxExpiry:     opts.MaxExpiry,
		onWake:        opts.OnWake,
		ctx:           ctx,
		cancel:        cancel,
		watches:       make(map[string]*armedWatch),
	}
	if m.digestWindow <= 0 {
		m.digestWindow = DefaultDigestWindow
	}
	if m.maxWatches <= 0 {
		m.maxWatches = DefaultMaxWatches
	}
	if m.defaultExpiry <= 0 {
		m.defaultExpiry = DefaultWatchExpiry
	}
	if m.maxExpiry <= 0 {
		m.maxExpiry = MaxWatchExpiry
	}
	if m.maxExpiry < m.defaultExpiry {
		m.maxExpiry = m.defaultExpiry
	}
	if m.deps.Now == nil {
		m.deps.Now = time.Now
	}
	return m
}

// SetOnWake installs the wake sink. It is called once during wiring, before
// any watch can fire; Rehydrate is called after it so a missed wake is not
// dropped on startup.
// validateDeps rejects a predicate whose source has no dependency in this
// session, so an isolated session's process watch fails at arm time instead
// of firing a "watch dropped" digest later.
func (m *Manager) validateDeps(src Source) error {
	switch src {
	case SourceJob:
		if m.deps.Jobs == nil {
			return fmt.Errorf("job predicates are not available in this session")
		}
	case SourceProcess:
		if m.deps.Processes == nil {
			return fmt.Errorf("process predicates are not available in this session (background_process is host-only)")
		}
	case SourceCommand:
		if m.deps.Exec == nil {
			return fmt.Errorf("command predicates are not available in this session")
		}
	case SourceHTTP:
		if m.deps.HTTP == nil {
			return fmt.Errorf("http predicates are not available in this session")
		}
	case SourceFile:
		if m.deps.Glob == nil {
			return fmt.Errorf("file predicates are not available in this session")
		}
	}
	return nil
}

func (m *Manager) SetOnWake(fn func(string)) {
	m.mu.Lock()
	m.onWake = fn
	m.mu.Unlock()
}

// Close stops every poller and drops undelivered digests. Fired watches stay
// in the store: the next resume reports them as missed wakes rather than
// losing them.
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		m.cancel()
		var flushDone chan struct{}
		m.mu.Lock()
		m.closed = true
		if m.timer != nil {
			if m.timer.Stop() {
				// The callback was prevented; Close owns its done.
				flushDone = m.flushDone
				m.flushDone = nil
				m.timer = nil
				if flushDone != nil {
					close(flushDone)
				}
			} else {
				// The callback already started and closes done itself in
				// finishFlush.
				flushDone = m.flushDone
			}
		}
		m.pending = nil
		// Armed rows stay in the store: the next resume re-arms them. The
		// in-memory map is dropped so a late List/Cancel cannot report a
		// poller that no longer exists.
		m.watches = make(map[string]*armedWatch)
		m.mu.Unlock()
		m.wg.Wait()
		// Join an in-flight digest callback so no onWake call can outlive
		// Close in the normal case. The wait is bounded: a view sink that
		// does a blocking network write must not hang teardown, and the
		// fired rows remain in the store for the next resume.
		if flushDone != nil {
			select {
			case <-flushDone:
			case <-time.After(digestCloseTimeout):
			}
		}
	})
}

// Arm starts a persistent watch. The predicate is validated before anything
// is stored or started, so a typo fails the call instead of arming a watch
// that can never fire.
func (m *Manager) Arm(req ArmRequest) (WatchInfo, error) {
	spec := Spec{On: req.On, Until: req.Until}
	src, _, _, err := parseSpec(spec)
	if err != nil {
		return WatchInfo{}, err
	}
	if err := m.validateDeps(src); err != nil {
		return WatchInfo{}, err
	}
	interval := intervalFor(src, req.Interval)
	expiresIn := req.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = m.defaultExpiry
	}
	if expiresIn > m.maxExpiry {
		expiresIn = m.maxExpiry
	}
	now := m.deps.now()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return WatchInfo{}, fmt.Errorf("session is shutting down")
	}
	id := req.ID
	if id == "" {
		for {
			m.nextID++
			id = fmt.Sprintf("w%d", m.nextID)
			if _, exists := m.watches[id]; !exists {
				break
			}
		}
	}
	if _, exists := m.watches[id]; exists {
		m.mu.Unlock()
		return WatchInfo{}, fmt.Errorf("a watch with id %q is already armed; cancel it or pick another id", id)
	}
	if len(m.watches) >= m.maxWatches {
		m.mu.Unlock()
		return WatchInfo{}, fmt.Errorf("watch limit reached (%d armed); cancel one with watch action=cancel", m.maxWatches)
	}
	w := &armedWatch{
		id:       id,
		spec:     spec,
		start:    now,
		expires:  now.Add(expiresIn),
		interval: interval,
		state:    StateArmed,
	}
	m.watches[id] = w
	rec := recordOf(w) // under m.mu: recordOf reads guarded fields
	info := infoOf(w)
	m.mu.Unlock()

	if m.store != nil {
		if serr := m.store.SaveWatch(m.sessionID, rec); serr != nil {
			m.remove(id)
			return WatchInfo{}, fmt.Errorf("persisting the watch: %w", serr)
		}
		// A Cancel can land between the insert and this save. It already
		// deleted the row; writing it first would leave a ghost watch that
		// the next resume re-arms. Re-check and remove the row it raced.
		m.mu.Lock()
		stillArmed := m.watches[id] == w
		m.mu.Unlock()
		if !stillArmed {
			_ = m.store.DeleteWatch(m.sessionID, id)
			return WatchInfo{}, fmt.Errorf("watch %q was cancelled while it was being armed", id)
		}
	}
	m.startWatch(w)
	return info, nil
}

// List returns every watch the manager still tracks, armed first and then by
// armed time. Fired watches remain visible until their digest is delivered.
func (m *Manager) List() []WatchInfo {
	m.mu.Lock()
	out := make([]WatchInfo, 0, len(m.watches))
	for _, w := range m.watches {
		out = append(out, infoOf(w)) // under m.mu: guarded fields
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].State != out[j].State {
			return out[i].State == StateArmed
		}
		if out[i].ArmedAt != out[j].ArmedAt {
			return out[i].ArmedAt < out[j].ArmedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Cancel removes an armed watch. It reports whether the id was known.
func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	w, ok := m.watches[id]
	if ok {
		delete(m.watches, id)
	}
	m.mu.Unlock()
	if !ok {
		return false
	}
	if w.cancel != nil {
		w.cancel()
	}
	if m.store != nil {
		_ = m.store.DeleteWatch(m.sessionID, id)
	}
	return true
}

// Rehydrate restores persisted watches after a session restart. A fired
// record is delivered as a missed wake; a predicate that can no longer be
// evaluated across processes (a job or process id) is reported the same way.
// Everything else is re-armed with its original armed time, so a time
// predicate that elapsed while the session was down fires immediately.
func (m *Manager) Rehydrate(ctx context.Context) error {
	if m.store == nil {
		return nil
	}
	rows, err := m.store.ListWatches(m.sessionID)
	if err != nil {
		return err
	}
	now := m.deps.now()
	var missed []firedWatch
	for _, row := range rows {
		spec := Spec{On: row.On, Until: row.Until}
		if row.State == StateFired {
			missed = append(missed, firedWatch{id: row.ID, spec: spec, evidence: row.Evidence, missed: true})
			continue
		}
		src, _, _, perr := parseSpec(spec)
		switch {
		case perr != nil:
			missed = append(missed, firedWatch{id: row.ID, spec: spec, evidence: "the persisted predicate is no longer valid: " + perr.Error(), missed: true})
		case src == SourceJob || src == SourceProcess:
			missed = append(missed, firedWatch{id: row.ID, spec: spec, evidence: fmt.Sprintf("%s ids are process-local; this watch could not be re-armed after the restart", src), missed: true})
		case row.ExpiresAt > 0 && now.After(time.Unix(0, row.ExpiresAt)):
			missed = append(missed, firedWatch{id: row.ID, spec: spec, evidence: "the watch expired while the session was down", missed: true})
		default:
			start := time.Unix(0, row.ArmedAt)
			expires := time.Unix(0, row.ExpiresAt)
			if row.ExpiresAt == 0 {
				expires = start.Add(m.defaultExpiry)
			}
			interval := intervalFor(src, time.Duration(row.IntervalMs)*time.Millisecond)
			w := &armedWatch{
				id:       row.ID,
				spec:     spec,
				start:    start,
				expires:  expires,
				interval: interval,
				state:    StateArmed,
			}
			m.mu.Lock()
			_, exists := m.watches[w.id]
			atCap := len(m.watches) >= m.maxWatches
			if !exists && !atCap {
				m.watches[w.id] = w
			}
			m.mu.Unlock()
			if exists {
				continue // already re-armed by an earlier pass
			}
			if atCap {
				// Do not drop a durable watch silently: the missed-wake
				// digest names it, and the user can re-arm it after
				// cancelling one.
				missed = append(missed, firedWatch{
					id: w.id, spec: spec,
					evidence: fmt.Sprintf("the session's watch limit (%d) was already met; this watch was not re-armed", m.maxWatches),
					missed:   true,
				})
				continue
			}
			m.startWatch(w)
		}
	}
	if len(missed) > 0 {
		// Mark the converted rows fired before delivering, so a crash
		// between delivery and deletion produces a missed wake rather
		// than silence.
		for i := range missed {
			w := &armedWatch{
				id:       missed[i].id,
				spec:     missed[i].spec,
				start:    now,
				state:    StateFired,
				evidence: missed[i].evidence,
				firedAt:  now,
			}
			if m.store != nil {
				_ = m.store.SaveWatch(m.sessionID, recordOf(w))
			}
		}
		m.enqueue(missed...)
	}
	return nil
}

func (m *Manager) startWatch(w *armedWatch) {
	ctx, cancel := context.WithDeadline(m.ctx, w.expires)
	// Publish the cancel func under the lock and re-check membership: a
	// Cancel that raced the gap between Arm's insert and this call has
	// already removed the watch, and starting its poller anyway would leak
	// it until expiry.
	m.mu.Lock()
	if m.closed || m.watches[w.id] != w {
		m.mu.Unlock()
		cancel()
		return
	}
	w.cancel = cancel
	// Add under the same lock as the closed check: Close sets closed and
	// then waits, so an Add cannot land after Wait has observed zero.
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer cancel()
		m.runWatch(ctx, w)
	}()
}

func (m *Manager) runWatch(ctx context.Context, w *armedWatch) {
	src, target, _, err := parseSpec(w.spec)
	if err != nil {
		m.fire(w, "watch dropped: "+err.Error(), false)
		return
	}

	// Time is the one predicate with a known deadline: sleep exactly to it
	// instead of polling, so a 4-minute wait is accurate to the millisecond.
	if src == SourceTime {
		dur, _ := time.ParseDuration(target)
		deadline := w.start.Add(dur)
		if wait := deadline.Sub(m.deps.now()); wait > 0 {
			select {
			case <-ctx.Done():
				if !m.deps.now().Before(w.expires) {
					m.fire(w, fmt.Sprintf("watch %s expired before its %s predicate elapsed", w.id, target), false)
				}
				return
			case <-m.deps.after(wait):
			}
		}
		fired, evidence, _ := m.deps.evaluate(ctx, w.spec, w.start)
		if fired {
			m.fire(w, evidence, false)
			return
		}
		m.fire(w, "watch expired before its predicate fired", false)
		return
	}

	// A job exposes a completion channel, so wait on it rather than polling.
	if src == SourceJob && m.deps.Jobs != nil {
		st, ok := m.deps.Jobs.Wait(ctx, target)
		if !ok {
			m.fire(w, fmt.Sprintf("watch dropped: unknown job_id %q", target), false)
			return
		}
		if !st.StillRunning {
			_, evidence, _ := m.deps.evaluate(ctx, w.spec, w.start)
			m.fire(w, evidence, false)
			return
		}
		m.fire(w, "watch expired before the job finished", false)
		return
	}

	interval := intervalFor(src, w.interval)
	for {
		fired, evidence, terminal := m.deps.evaluate(ctx, w.spec, w.start)
		if fired {
			m.fire(w, evidence, false)
			return
		}
		if ctx.Err() != nil {
			// An evaluation that observed the deadline can surface it as a
			// terminal error; expiry is the honest label, not "dropped".
			m.fire(w, "watch expired before its predicate fired", false)
			return
		}
		if terminal != nil {
			m.fire(w, "watch dropped: "+terminal.Error(), false)
			return
		}
		delay := interval
		if remaining := w.expires.Sub(m.deps.now()); remaining > 0 && remaining < delay {
			delay = remaining
		}
		if delay <= 0 {
			delay = time.Millisecond
		}
		select {
		case <-ctx.Done():
			m.fire(w, "watch expired before its predicate fired", false)
			return
		case <-m.deps.after(delay):
		}
		interval = nextInterval(interval, MaxIntervalFor(src))
	}
}

// fire records the watch as fired, persists it, and queues it for the
// batching digest. A watch that is cancelled between firing and delivery is
// still delivered: the predicate did fire, and silently dropping it would
// lose the fact the orchestrator was waiting on.
func (m *Manager) fire(w *armedWatch, evidence string, missed bool) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	cur, ok := m.watches[w.id]
	if !ok || cur != w {
		// Cancelled (or already delivered and reaped): a poller that was
		// mid-evaluation must not resurrect it with a spurious digest.
		m.mu.Unlock()
		return
	}
	if cur.state == StateFired {
		m.mu.Unlock()
		return
	}
	w.state = StateFired
	w.evidence = evidence
	w.firedAt = m.deps.now()
	rec := recordOf(w) // under m.mu
	m.mu.Unlock()

	if m.store != nil {
		_ = m.store.SaveWatch(m.sessionID, rec)
	}
	m.enqueue(firedWatch{id: w.id, spec: w.spec, evidence: evidence, missed: missed, w: w})
}

func (m *Manager) enqueue(ws ...firedWatch) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.pending = append(m.pending, ws...)
	if m.timer == nil {
		m.scheduleFlushLocked()
	}
	m.mu.Unlock()
}

// scheduleFlushLocked starts the digest timer when none is pending. The
// caller must hold m.mu and have checked m.closed.
func (m *Manager) scheduleFlushLocked() {
	done := make(chan struct{})
	m.flushDone = done
	m.timer = m.deps.afterFunc(m.digestWindow, func() { m.flushIfOpen(done) })
}

// finishFlush closes the callback's done channel and starts another digest if
// fires arrived while this one was delivering. Only one callback runs at a
// time: m.timer stays non-nil until here, so enqueue appends rather than
// starting a second callback.
func (m *Manager) finishFlush(done chan struct{}) {
	m.mu.Lock()
	m.timer = nil
	if m.flushDone == done {
		m.flushDone = nil
	}
	close(done)
	if !m.closed && len(m.pending) > 0 {
		m.scheduleFlushLocked()
	}
	m.mu.Unlock()
}

// flushIfOpen runs on the digest timer. It re-checks closed so a timer that
// fires during Close leaves the fired records durable for the next resume.
func (m *Manager) flushIfOpen(done chan struct{}) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		m.finishFlush(done)
		return
	}
	pending := m.pending
	m.pending = nil
	onWake := m.onWake
	m.mu.Unlock()

	if len(pending) > 0 && onWake != nil {
		onWake(digestText(pending))
	}

	m.mu.Lock()
	if m.closed {
		// Close won the race: keep the fired rows so the next resume
		// reports them as missed wakes instead of dropping a digest whose
		// delivery may have been cut off by teardown.
		m.mu.Unlock()
		m.finishFlush(done)
		return
	}
	for _, p := range pending {
		if p.w != nil && m.watches[p.id] == p.w {
			delete(m.watches, p.id)
		}
	}
	m.mu.Unlock()

	if m.store != nil {
		for _, p := range pending {
			// Never delete a same-id row that a newer watch now owns. A
			// fired watch is matched by pointer; a missed-wake entry
			// (p.w == nil) has no in-memory record, so an id that
			// reappeared belongs to a new arm and its row must survive.
			m.mu.Lock()
			current := m.watches[p.id]
			_, reused := m.watches[p.id]
			m.mu.Unlock()
			if p.w != nil && current != nil && current != p.w {
				continue
			}
			if p.w == nil && reused {
				continue
			}
			_ = m.store.DeleteWatch(m.sessionID, p.id)
		}
	}
	m.finishFlush(done)
}

func (m *Manager) remove(id string) {
	m.mu.Lock()
	w := m.watches[id]
	delete(m.watches, id)
	m.mu.Unlock()
	if w != nil && w.cancel != nil {
		w.cancel()
	}
}

// digestText renders one wake message for a batch of simultaneous fires.
// Multiple fires are deliberately one digest: an orchestrator that armed
// three CI checks should be woken once, not three times.
func digestText(pending []firedWatch) string {
	var b strings.Builder
	b.WriteString("[WATCH] ")
	switch len(pending) {
	case 1:
		b.WriteString("armed watch fired")
	default:
		fmt.Fprintf(&b, "%d armed watches fired", len(pending))
	}
	b.WriteString(":\n")
	for _, p := range pending {
		fmt.Fprintf(&b, "- %s (%s", p.id, p.spec.On)
		if p.spec.Until != "" {
			fmt.Fprintf(&b, " until %s", p.spec.Until)
		}
		b.WriteString(")")
		if p.missed {
			b.WriteString(" [fired while the session was away]")
		}
		b.WriteString(": ")
		b.WriteString(clip(strings.ReplaceAll(p.evidence, "\n", "\n  "), 800))
		b.WriteString("\n")
	}
	b.WriteString("This is a harness wake, not a message from the user. Continue the task from where you left off; do not repeat polling that this wake replaces.")
	return clip(b.String(), 6000)
}

func recordOf(w *armedWatch) WatchRecord {
	rec := WatchRecord{
		ID:         w.id,
		On:         w.spec.On,
		Until:      w.spec.Until,
		State:      w.state,
		Evidence:   w.evidence,
		ArmedAt:    w.start.UnixNano(),
		IntervalMs: w.interval.Milliseconds(),
	}
	if !w.expires.IsZero() {
		rec.ExpiresAt = w.expires.UnixNano()
	}
	if !w.firedAt.IsZero() {
		rec.FiredAt = w.firedAt.UnixNano()
	}
	return rec
}

func infoOf(w *armedWatch) WatchInfo {
	info := WatchInfo{
		ID:         w.id,
		On:         w.spec.On,
		Until:      w.spec.Until,
		State:      w.state,
		Evidence:   w.evidence,
		ArmedAt:    w.start.UnixNano(),
		IntervalMs: w.interval.Milliseconds(),
	}
	if !w.expires.IsZero() {
		info.ExpiresAt = w.expires.UnixNano()
	}
	return info
}

func clampInterval(d time.Duration) time.Duration {
	if d < MinInterval {
		return MinInterval
	}
	if d > MaxInterval {
		return MaxInterval
	}
	return d
}

func nextInterval(d, max time.Duration) time.Duration {
	d *= 2
	if max > 0 && d > max {
		d = max
	}
	if d > MaxInterval {
		d = MaxInterval
	}
	return d
}
