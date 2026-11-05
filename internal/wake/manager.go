package wake

import (
	"context"
	"fmt"
	"os"
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
		observer:      opts.Observer,
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
	m.ownerID = opts.OwnerID
	if m.ownerID == "" {
		m.ownerID = fmt.Sprintf("pid-%d", os.Getpid())
	}
	m.leaseDuration = opts.LeaseDuration
	if m.leaseDuration <= 0 {
		m.leaseDuration = DefaultLeaseDuration
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

// Close stops every poller and drops undelivered digests in this process.
// Fired rows stay in the store and their short delivery lease is released, so
// the next resume reports them as missed wakes rather than losing them.
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		m.cancel()
		var flushDone chan struct{}
		var firedIDs []string
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
		// Collect fired rows before dropping the map: their store leases
		// must be released so a resume after this graceful close reports an
		// undelivered digest instead of suppressing it.
		seen := make(map[string]bool, len(m.watches)+len(m.pending))
		for id, w := range m.watches {
			if w.state == StateFired && !seen[id] {
				firedIDs = append(firedIDs, id)
				seen[id] = true
			}
		}
		for _, p := range m.pending {
			if !seen[p.id] {
				firedIDs = append(firedIDs, p.id)
				seen[p.id] = true
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
		if m.store != nil {
			for _, id := range firedIDs {
				// A delivered row was deleted already (no-op); an
				// undelivered one becomes visible to the next resume.
				_, _ = m.store.ReleaseWatchLease(m.sessionID, id, m.ownerID)
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
		owner:    m.ownerID,
		lease:    now.Add(m.leaseDuration),
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
	m.observeWatchArmed(info)
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
	m.observeWatchCancelled(id)
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
	now := m.deps.now()
	// Claim every row before rebuilding it. Last resume wins: a later
	// process takes over, and this process's ClaimFire calls then fail, so
	// two processes cannot both deliver the same watch.
	rows, err := m.store.ClaimWatches(m.sessionID, m.ownerID, now.Add(m.leaseDuration).UnixNano())
	if err != nil {
		return err
	}
	var missed []firedWatch
	// Fired rows are not claimed: a live owner may still be delivering
	// them. Re-deliver one only after the previous owner's lease expired.
	firedRows, err := m.store.ListWatches(m.sessionID)
	if err != nil {
		return err
	}
	for _, row := range firedRows {
		if row.State != StateFired {
			continue
		}
		if row.LeaseUntil > now.UnixNano() {
			continue // previous owner still holds a live lease
		}
		missed = append(missed, firedWatch{
			id: row.ID, spec: Spec{On: row.On, Until: row.Until},
			evidence: row.Evidence, missed: true,
			armedAt: row.ArmedAt, expiresAt: row.ExpiresAt, intervalMs: row.IntervalMs,
		})
	}
	for _, row := range rows {
		spec := Spec{On: row.On, Until: row.Until}
		src, _, _, perr := parseSpec(spec)
		base := firedWatch{
			id: row.ID, spec: spec, missed: true,
			armedAt: row.ArmedAt, expiresAt: row.ExpiresAt, intervalMs: row.IntervalMs,
		}
		switch {
		case perr != nil:
			base.evidence = Evidence{Kind: "watch", Status: "dropped", Detail: "the persisted predicate is no longer valid: " + perr.Error()}
			missed = append(missed, base)
		case src == SourceJob || src == SourceProcess:
			base.evidence = Evidence{Kind: "watch", Status: "dropped", Detail: fmt.Sprintf("%s ids are process-local; this watch could not be re-armed after the restart", src)}
			missed = append(missed, base)
		case row.ExpiresAt > 0 && now.After(time.Unix(0, row.ExpiresAt)):
			base.evidence = Evidence{Kind: "watch", Status: "expired", Detail: "the watch expired while the session was down"}
			missed = append(missed, base)
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
				owner:    m.ownerID,
				lease:    now.Add(m.leaseDuration),
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
				base.evidence = Evidence{Kind: "watch", Status: "dropped", Detail: fmt.Sprintf("the session's watch limit (%d) was already met; this watch was not re-armed", m.maxWatches)}
				missed = append(missed, base)
				continue
			}
			// Snapshot before the poller starts: a fast fire writes
			// w.evidence under m.mu, and infoOf reads it outside the lock.
			info := infoOf(w)
			m.observeWatchArmed(info)
			m.startWatch(w)
		}
	}
	if len(missed) > 0 {
		// Mark the converted rows fired before delivering, so a crash
		// between delivery and deletion produces a missed wake rather
		// than silence.
		for i := range missed {
			w := &armedWatch{
				id:    missed[i].id,
				spec:  missed[i].spec,
				start: now,
				owner: m.ownerID,
				// No lease: if this process closes before the digest is
				// delivered, the next resume must report the row again
				// rather than suppress it.
				state:    StateFired,
				evidence: missed[i].evidence,
				firedAt:  now,
			}
			if m.store != nil {
				_ = m.store.SaveWatch(m.sessionID, recordOf(w))
			}
		}
		for i := range missed {
			ev := missed[i].evidence
			m.observeWatchFired(WatchInfo{
				ID:         missed[i].id,
				On:         missed[i].spec.On,
				Until:      missed[i].spec.Until,
				State:      StateFired,
				Evidence:   &ev,
				ArmedAt:    missed[i].armedAt,
				ExpiresAt:  missed[i].expiresAt,
				IntervalMs: missed[i].intervalMs,
			}, true)
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
		m.fire(w, Evidence{Kind: "watch", Status: "dropped", Detail: err.Error()}, false)
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
					m.fire(w, Evidence{Kind: "watch", Status: "expired", Detail: fmt.Sprintf("watch %s expired before its %s predicate elapsed", w.id, target)}, false)
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
		m.fire(w, Evidence{Kind: "watch", Status: "expired", Detail: "watch expired before its predicate fired"}, false)
		return
	}

	// A job exposes a completion channel, so wait on it rather than polling.
	if src == SourceJob && m.deps.Jobs != nil {
		st, ok := m.deps.Jobs.Wait(ctx, target)
		if !ok {
			m.fire(w, Evidence{Kind: "watch", Status: "dropped", Detail: fmt.Sprintf("unknown job_id %q", target)}, false)
			return
		}
		if !st.StillRunning {
			_, evidence, _ := m.deps.evaluate(ctx, w.spec, w.start)
			m.fire(w, evidence, false)
			return
		}
		m.fire(w, Evidence{Kind: "watch", Status: "expired", Detail: "watch expired before the job finished"}, false)
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
			m.fire(w, Evidence{Kind: "watch", Status: "expired", Detail: "watch expired before its predicate fired"}, false)
			return
		}
		if terminal != nil {
			m.fire(w, Evidence{Kind: "watch", Status: "dropped", Detail: terminal.Error()}, false)
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
			m.fire(w, Evidence{Kind: "watch", Status: "expired", Detail: "watch expired before its predicate fired"}, false)
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
func (m *Manager) fire(w *armedWatch, evidence Evidence, missed bool) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	cur, ok := m.watches[w.id]
	if !ok || cur != w || cur.state == StateFired {
		// Cancelled (or already delivered and reaped): a poller that was
		// mid-evaluation must not resurrect it with a spurious digest.
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	// Claim the fire in the store before delivering. A process that lost the
	// last-resume race gets false here; it drops the watch instead of
	// duplicating the wake the current owner will deliver. A store error is
	// treated as best-effort success: a duplicate is better than a lost wake.
	if m.store != nil {
		now := m.deps.now()
		claimed, err := m.store.ClaimFire(
			m.sessionID, w.id, m.ownerID,
			w.start.UnixNano(), now.UnixNano(), now.Add(firedLeaseDuration).UnixNano(),
			MarshalEvidence(evidence))
		if err == nil && !claimed {
			m.removeIf(w)
			return
		}
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	cur, ok = m.watches[w.id]
	if !ok || cur != w || cur.state == StateFired {
		m.mu.Unlock()
		return
	}
	w.state = StateFired
	w.evidence = evidence
	w.firedAt = m.deps.now()
	info := infoOf(w)
	m.mu.Unlock()

	m.observeWatchFired(info, missed)
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

// removeIf removes w only while it is still the current record for its id, so
// a lost claim cannot drop a same-id replacement armed after it.
func (m *Manager) removeIf(w *armedWatch) {
	m.mu.Lock()
	if m.watches[w.id] != w {
		m.mu.Unlock()
		return
	}
	delete(m.watches, w.id)
	m.mu.Unlock()
	if w.cancel != nil {
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
		rendered := p.evidence.Render()
		if rendered == "" {
			rendered = "no evidence"
		}
		b.WriteString(clip(strings.ReplaceAll(rendered, "\n", "\n  "), 800))
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
		Owner:      w.owner,
	}
	if !w.expires.IsZero() {
		rec.ExpiresAt = w.expires.UnixNano()
	}
	if !w.lease.IsZero() {
		rec.LeaseUntil = w.lease.UnixNano()
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
		ArmedAt:    w.start.UnixNano(),
		IntervalMs: w.interval.Milliseconds(),
	}
	if !w.evidence.IsZero() {
		ev := w.evidence
		info.Evidence = &ev
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
