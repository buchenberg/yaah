package wake

import (
	"context"
	"time"
)

// The observe helpers keep nil checks out of the lifecycle code. An observer
// is optional; the engine behaves identically without one.

func (m *Manager) observeWaitStarted(ctx context.Context, spec Spec, timeout time.Duration) context.Context {
	if m.observer == nil {
		return ctx
	}
	return m.observer.WaitStarted(ctx, spec, timeout)
}

func (m *Manager) observeWaitFinished(ctx context.Context, spec Spec, fired, timedOut, interrupted, errored bool, elapsed time.Duration) {
	if m.observer == nil {
		return
	}
	m.observer.WaitFinished(ctx, spec, fired, timedOut, interrupted, errored, elapsed)
}

func (m *Manager) observeWatchArmed(info WatchInfo) {
	if m.observer != nil {
		m.observer.WatchArmed(info)
	}
}

func (m *Manager) observeWatchFired(info WatchInfo, missed bool) {
	if m.observer != nil {
		m.observer.WatchFired(info, missed)
	}
}

func (m *Manager) observeWatchCancelled(id string) {
	if m.observer != nil {
		m.observer.WatchCancelled(id)
	}
}
