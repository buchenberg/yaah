package wake

import "time"

// Timer is the subset of *time.Timer the manager needs to cancel the digest
// timer: Stop reports whether the callback was prevented.
type Timer interface {
	Stop() bool
}

// Clock is the time seam for the engine. Production uses the real clock; tests
// inject a fake so polling and digest windows do not sleep in wall time.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
	AfterFunc(d time.Duration, f func()) Timer
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (realClock) AfterFunc(d time.Duration, f func()) Timer {
	return time.AfterFunc(d, f)
}

// after and afterFunc fall back to the real clock when Deps carries no Clock,
// so existing callers (and tests that only pin Now) keep working.
func (d Deps) after(t time.Duration) <-chan time.Time {
	if d.Clock != nil {
		return d.Clock.After(t)
	}
	return time.After(t)
}

func (d Deps) afterFunc(t time.Duration, f func()) Timer {
	if d.Clock != nil {
		return d.Clock.AfterFunc(t, f)
	}
	return time.AfterFunc(t, f)
}
