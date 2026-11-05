package wake

import (
	"context"
	"fmt"
	"time"
)

// Wait blocks until the predicate fires, the per-call timeout elapses, or the
// caller's context ends. A timeout is a structured result with a resumable
// handle, not an error; only an unusable predicate is an error.
func (m *Manager) Wait(ctx context.Context, req WaitRequest) (res Result, err error) {
	spec := Spec{On: req.On, Until: req.Until}
	start := m.deps.now()
	if req.Handle != "" {
		hs, hstart, err := decodeHandle(req.Handle, spec)
		if err != nil {
			return Result{}, err
		}
		spec, start = hs, hstart
	}
	src, target, _, err := parseSpec(spec)
	if err != nil {
		return Result{}, err
	}
	if err := m.validateDeps(src); err != nil {
		return Result{}, err
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultWaitTimeout
	}
	if timeout > MaxWaitTimeout {
		timeout = MaxWaitTimeout
	}
	interval := intervalFor(src, req.Interval)

	ctx = m.observeWaitStarted(ctx, spec, timeout)
	defer func() {
		m.observeWaitFinished(ctx, spec,
			err == nil && res.Fired,
			err == nil && res.TimedOut,
			err == nil && res.Interrupted,
			err != nil,
			m.deps.now().Sub(start))
	}()
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	elapsed := func() int64 { return m.deps.now().Sub(start).Milliseconds() }
	// lastEvidence is reported with a timeout or interruption, so a caller
	// that resumes the wait still sees what the last attempt observed.
	var lastEvidence Evidence

loop:
	for {
		if src == SourceJob && m.deps.Jobs != nil {
			st, ok := m.deps.Jobs.Wait(waitCtx, target)
			if !ok {
				return Result{}, fmt.Errorf("unknown job_id %q: it was never dispatched in this session, or its finished record was reaped", target)
			}
			if !st.StillRunning {
				_, evidence, _ := m.deps.evaluate(waitCtx, spec, start)
				return Result{
					Fired:     true,
					Source:    string(src),
					On:        spec.On,
					Evidence:  &evidence,
					ElapsedMs: elapsed(),
				}, nil
			}
			lastEvidence = Evidence{Kind: "job", Status: "running", Detail: fmt.Sprintf("job %s is still running", target)}
			break // the wait context ended with the job still running
		}

		fired, evidence, terminal := m.deps.evaluate(waitCtx, spec, start)
		if !evidence.IsZero() {
			lastEvidence = evidence
		}
		if terminal != nil {
			if waitCtx.Err() != nil {
				break
			}
			return Result{}, terminal
		}
		if fired {
			return Result{
				Fired:     true,
				Source:    string(src),
				On:        spec.On,
				Evidence:  &evidence,
				ElapsedMs: elapsed(),
			}, nil
		}

		delay := interval
		if src == SourceTime {
			if dur, derr := time.ParseDuration(target); derr == nil {
				if remaining := dur - m.deps.now().Sub(start); remaining > 0 && remaining < delay {
					delay = remaining
				}
			}
		}
		if delay <= 0 {
			delay = time.Millisecond
		}
		select {
		case <-waitCtx.Done():
			break loop
		case <-m.deps.after(delay):
		}
		interval = nextInterval(interval, MaxIntervalFor(src))
	}

	res = Result{
		Source:    string(src),
		On:        spec.On,
		ElapsedMs: elapsed(),
		Handle:    encodeHandle(spec, start),
	}
	if !lastEvidence.IsZero() {
		res.Evidence = &lastEvidence
	}
	if ctx.Err() != nil {
		res.Interrupted = true
		res.Notes = fmt.Sprintf("wait interrupted by the caller's context (%v) before the predicate fired; the predicate was not changed", ctx.Err())
	} else {
		res.TimedOut = true
		res.Notes = "the predicate had not fired when timeout_seconds elapsed; call wait_for again with this handle to keep waiting from the same start"
	}
	return res, nil
}
