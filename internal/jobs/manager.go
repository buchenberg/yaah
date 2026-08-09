package jobs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/types"
)

// Background job lifecycle states reported by BackgroundJobStatus.Status.
const (
	BGStatusRunning   = "running"
	BGStatusCompleted = "completed"
	BGStatusFailed    = "failed"
	BGStatusCancelled = "cancelled"
)

// BackgroundJobStatus is a serializable snapshot of one background
// sub-agent job, used by the subagent_jobs lifecycle tool.
type BackgroundJobStatus struct {
	ID          string `json:"id"`
	Role        string `json:"role"`
	Description string `json:"description"`
	Model       string `json:"model,omitempty"`
	Status      string `json:"status"`
	DurationMs  int64  `json:"duration_ms,omitempty"`
	Error       string `json:"error,omitempty"`
}

// backgroundJob is the live, mutable tracking record for one background
// sub-agent. Callers interact with it only through BackgroundJobs methods.
type backgroundJob struct {
	id          string
	role        string
	description string
	startedAt   time.Time

	// model and usage are written by the runner via context-injected
	// pointers (WithSubAgentModelPtr / WithSubAgentUsage) so the runner
	// closure does not need to know it is running in the background.
	model string
	usage types.Usage

	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	finished bool
	status   string
	err      error
	duration time.Duration
}

func (j *backgroundJob) snapshot() BackgroundJobStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	st := j.status
	dur := j.duration
	if !j.finished {
		st = BGStatusRunning
		dur = time.Since(j.startedAt)
	}
	s := BackgroundJobStatus{
		ID:          j.id,
		Role:        j.role,
		Description: j.description,
		Model:       j.model,
		Status:      st,
		DurationMs:  dur.Milliseconds(),
	}
	if j.finished && j.err != nil {
		s.Error = j.err.Error()
	}
	return s
}

// BackgroundJobs manages in-flight background sub-agents for a session.
//
// It owns a root context (cancelled on Close) that every background job
// derives its cancellation from. This decouples a background sub-agent's
// lifetime from the tool call and turn that dispatched it — the fix for
// the prior bug where the foreground tool-call context was cancelled the
// instant Execute returned, killing the "detached" job almost
// immediately.
//
// The agent controls background job lifecycle via the subagent_jobs tool
// (list, status, cancel, wait). Jobs that finish between turns publish
// no UI event (the loop's broker is closed), but their state is preserved
// for status lookups. CancelPending() is available for callers that need
// explicit bulk cancellation.
//
// Trace parentage is preserved: each job's sub-agent span is created as a
// child of the dispatching call's span (while that span is still alive),
// then carried on the job's context via trace.ContextWithSpan, so the
// job's work appears correctly in the originating trace even though it
// outlives the dispatch span.
//
// Safe for concurrent use.
type BackgroundJobs struct {
	rootCtx    context.Context
	rootCancel context.CancelFunc
	done       chan struct{} // closed on Close

	mu     sync.Mutex
	nextID int
	jobs   map[string]*backgroundJob

	// MaxConcurrent bounds the number of simultaneously running
	// background jobs. Zero disables the limit. When at capacity, Launch
	// returns an error so the orchestrator can wait or cancel.
	MaxConcurrent int

	// maxKept bounds the number of finished jobs retained for status
	// lookups. Older finished jobs are reaped. Zero keeps them all (the
	// caller may set a bound).
	maxKept int

	// Deliver is called (in the completing job's goroutine) with the
	// final result/error. Wired to push onto the follow-up channel.
	// Implementations MUST be safe to call after Close (e.g. select on
	// Done()) so a job completing during shutdown does not block forever.
	Deliver func(role, description, result string, err error)

	// OnUsage attributes a completed job's token usage to a persistent
	// accumulator (typically the session total). Session-scoped: always
	// on, so usage is never lost across turns.
	OnUsage func(types.Usage)

	// OnStart / OnEnd are optional event hooks (e.g. publishing
	// SubAgentStartEvent / SubAgentEndEvent to the live loop broker).
	// The id is the background job's sub-agent identifier ("bg-N").
	// Loop-scoped: the loop registers them at Run start and clears them
	// at Run end, so events only fire while a loop is live. Nil between
	// runs is fine — the job keeps running; only the UI event is skipped.
	OnStart func(id, role, model, prompt string)
	OnEnd   func(id, role, model, prompt, result string, dur time.Duration, err string)
}

// NewBackgroundJobs creates a manager backed by context.Background().
// The caller must Close it (typically at session end) to cancel any
// still-running jobs.
func NewBackgroundJobs() *BackgroundJobs {
	ctx, cancel := context.WithCancel(context.Background())
	return &BackgroundJobs{
		rootCtx:    ctx,
		rootCancel: cancel,
		done:       make(chan struct{}),
		jobs:       make(map[string]*backgroundJob),
		maxKept:    256,
	}
}

// Done returns a channel closed when Close is called. Delivery loops
// select on it to avoid blocking forever after the session ends.
func (m *BackgroundJobs) Done() <-chan struct{} { return m.done }

// Close cancels the root context (stopping in-flight jobs) and unblocks
// any blocked Deliver calls. Idempotent.
func (m *BackgroundJobs) Close() {
	m.rootCancel()
	m.mu.Lock()
	select {
	case <-m.done:
	default:
		close(m.done)
	}
	m.mu.Unlock()
}

// Launch starts a background sub-agent and returns its job id.
//
// callCtx supplies the trace parent (its span must still be alive) and is
// NOT used for cancellation — the job derives cancellation from the
// session root so it survives the dispatching tool call and turn. The
// runner closure writes the resolved model and accumulated usage back
// through context-injected pointers set up here.
func (m *BackgroundJobs) Launch(callCtx context.Context, runner TaskRunner, role, description, prompt string, params SubAgentParams, timeout time.Duration) (string, error) {
	m.mu.Lock()
	if m.MaxConcurrent > 0 {
		running := 0
		for _, j := range m.jobs {
			j.mu.Lock()
			if !j.finished {
				running++
			}
			j.mu.Unlock()
		}
		if running >= m.MaxConcurrent {
			m.mu.Unlock()
			return "", fmt.Errorf("spawn_subagent: background concurrency limit (%d) reached; wait for a running job to finish or cancel one with subagent_jobs", m.MaxConcurrent)
		}
	}
	m.nextID++
	id := fmt.Sprintf("bg-%d", m.nextID)
	m.mu.Unlock()

	// Create the sub-agent span as a child of the dispatching call's
	// span while it is still alive. We keep this span object and carry
	// it on the job context below so the job's work is parented to the
	// originating trace even after the dispatch span ends.
	_, span := observability.StartSubAgent(callCtx, role, description)

	// Cancellation comes from the session root (survives the call/turn),
	// optionally bounded by the job timeout.
	jobCtx, jobCancel := context.WithCancel(m.rootCtx)
	if timeout > 0 {
		jobCtx, jobCancel = context.WithTimeout(jobCtx, timeout)
	}
	// Carry the sub-agent span so child spans created inside the runner
	// are parented to it, and so the runner can set span attributes.
	jobCtx = trace.ContextWithSpan(jobCtx, span)

	job := &backgroundJob{
		id:          id,
		role:        role,
		description: description,
		startedAt:   time.Now(),
		cancel:      jobCancel,
		done:        make(chan struct{}),
	}
	// Let the runner closure report its model and usage back into the job.
	jobCtx = WithSubAgentModelPtr(jobCtx, &job.model)
	jobCtx = WithSubAgentUsage(jobCtx, &job.usage)
	jobCtx = WithSubAgentStartNotifier(jobCtx, func(model string) {
		job.model = model
		if m.OnStart != nil {
			m.OnStart(job.id, role, model, description)
		}
	})

	m.mu.Lock()
	m.jobs[id] = job
	m.mu.Unlock()

	go m.run(job, jobCtx, jobCancel, span, runner, prompt, params)

	return id, nil
}

// run executes the runner to completion, then finalizes the job's state,
// tracing, usage, events, and result delivery.
func (m *BackgroundJobs) run(job *backgroundJob, jobCtx context.Context, jobCancel context.CancelFunc, span trace.Span, runner TaskRunner, prompt string, params SubAgentParams) {
	defer jobCancel()
	defer close(job.done)

	result, runErr := runner(jobCtx, prompt, params)
	if jobCtx.Err() != nil && runErr == nil {
		runErr = jobCtx.Err()
	}

	job.mu.Lock()
	job.finished = true
	job.duration = time.Since(job.startedAt)
	switch {
	case jobCtx.Err() == context.Canceled || errors.Is(runErr, context.Canceled):
		job.status = BGStatusCancelled
	case errors.Is(runErr, context.DeadlineExceeded):
		job.status = BGStatusFailed
	case runErr != nil:
		job.status = BGStatusFailed
	default:
		job.status = BGStatusCompleted
	}
	job.err = runErr
	job.mu.Unlock()

	if span != nil && span.IsRecording() {
		observability.FinishSubAgent(span, runErr)
	}
	if span != nil {
		span.End()
	}

	if m.OnUsage != nil && (job.usage.TotalTokens > 0 || job.usage.PromptTokens > 0 || job.usage.CompletionTokens > 0) {
		m.OnUsage(job.usage)
	}
	if m.OnEnd != nil {
		errStr := ""
		if runErr != nil {
			errStr = runErr.Error()
		}
		m.OnEnd(job.id, job.role, job.model, job.description, result, job.duration, errStr)
	}
	if m.Deliver != nil {
		m.Deliver(job.role, job.description, result, runErr)
	}

	m.reapFinished()
}

// reapFinished trims the retained set of finished jobs to maxKept so a
// long session does not grow the jobs map without bound.
func (m *BackgroundJobs) reapFinished() {
	if m.maxKept <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.jobs) <= m.maxKept {
		return
	}
	type entry struct {
		id string
		t  time.Time
	}
	var finished []entry
	for id, j := range m.jobs {
		j.mu.Lock()
		f := j.finished
		t := j.startedAt
		j.mu.Unlock()
		if f {
			finished = append(finished, entry{id, t})
		}
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].t.Before(finished[j].t) })
	excess := len(m.jobs) - m.maxKept
	for i := 0; i < excess && i < len(finished); i++ {
		delete(m.jobs, finished[i].id)
	}
}

// Status returns a snapshot of the job with the given id. The second
// value is false if no such job is known (never existed or was reaped).
func (m *BackgroundJobs) Status(id string) (BackgroundJobStatus, bool) {
	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return BackgroundJobStatus{}, false
	}
	return j.snapshot(), true
}

// List returns snapshots of all known jobs, newest first.
func (m *BackgroundJobs) List() []BackgroundJobStatus {
	m.mu.Lock()
	jobs := make([]*backgroundJob, 0, len(m.jobs))
	for _, j := range m.jobs {
		jobs = append(jobs, j)
	}
	m.mu.Unlock()
	out := make([]BackgroundJobStatus, len(jobs))
	for i, j := range jobs {
		out[i] = j.snapshot()
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Status == out[j].Status {
			return out[i].ID > out[j].ID
		}
		// running jobs first
		return out[i].Status == BGStatusRunning && out[j].Status != BGStatusRunning
	})
	return out
}

// Cancel requests cancellation of the job with the given id. Returns
// false if no such job is known. The job's goroutine observes the
// cancellation through its context.
func (m *BackgroundJobs) Cancel(id string) bool {
	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return false
	}
	j.cancel()
	return true
}

// CancelPending cancels all currently running (unfinished) background
// jobs. Returns immediately — callers should not wait for job completion.
// Use this at turn boundaries to prevent orphaned background work from
// surviving the agent loop that dispatched it.
func (m *BackgroundJobs) CancelPending() {
	m.mu.Lock()
	for _, j := range m.jobs {
		j.mu.Lock()
		if !j.finished {
			j.cancel()
		}
		j.mu.Unlock()
	}
	m.mu.Unlock()
}

// Wait blocks until the job with the given id finishes or ctx is
// cancelled. Returns the job snapshot and false if the job is unknown.
func (m *BackgroundJobs) Wait(ctx context.Context, id string) (BackgroundJobStatus, bool) {
	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return BackgroundJobStatus{}, false
	}
	select {
	case <-j.done:
	case <-ctx.Done():
	}
	return j.snapshot(), true
}

// Pending returns the count of jobs that have not yet finished.
func (m *BackgroundJobs) Pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, j := range m.jobs {
		j.mu.Lock()
		if !j.finished {
			n++
		}
		j.mu.Unlock()
	}
	return n
}
