package jobs

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/types"
)

// jobResult captures a delivery for test inspection.
type jobResult struct {
	role string
	desc string
	res  string
	err  error
}

func newTestManager() *BackgroundJobs {
	m := NewBackgroundJobs()
	m.maxKept = 0 // disable reaping so tests can inspect history
	return m
}

// TestBackgroundJobs_LaunchAndDeliver verifies a launched job runs to
// completion and its result is delivered.
func TestBackgroundJobs_LaunchAndDeliver(t *testing.T) {
	m := newTestManager()
	defer m.Close()

	var got jobResult
	var mu sync.Mutex
	m.Deliver = func(role, desc, res string, err error) {
		mu.Lock()
		got = jobResult{role, desc, res, err}
		mu.Unlock()
	}

	runner := func(ctx context.Context, prompt string, p SubAgentParams) (string, error) {
		WriteSubAgentModel(ctx, "test-model")
		AddSubAgentUsage(ctx, types.Usage{TotalTokens: 42})
		return "done: " + prompt, nil
	}

	id, err := m.Launch(context.Background(), runner, "analyst", "summarize", "read the file", SubAgentParams{Role: "analyst"}, 0)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty job id")
	}

	waitForJob(t, m, id, 2*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if got.res != "done: read the file" {
		t.Errorf("delivered result = %q, want %q", got.res, "done: read the file")
	}
	if got.err != nil {
		t.Errorf("delivered err = %v, want nil", got.err)
	}
	if got.role != "analyst" {
		t.Errorf("delivered role = %q, want analyst", got.role)
	}

	st, ok := m.Status(id)
	if !ok {
		t.Fatal("job not found after completion")
	}
	if st.Status != BGStatusCompleted {
		t.Errorf("status = %q, want completed", st.Status)
	}
	if st.Model != "test-model" {
		t.Errorf("model = %q, want test-model", st.Model)
	}
}

// TestBackgroundJobs_SurvivesCallContextCancellation is the core
// regression test: the background job must NOT be cancelled when the
// dispatching (foreground) context is cancelled. This is the exact bug
// that made background sub-agents die almost immediately.
func TestBackgroundJobs_SurvivesCallContextCancellation(t *testing.T) {
	m := newTestManager()
	defer m.Close()

	var completed atomic.Bool
	m.Deliver = func(role, desc, res string, err error) {
		completed.Store(true)
	}

	runner := func(ctx context.Context, prompt string, p SubAgentParams) (string, error) {
		// Simulate work that takes longer than the call context lives.
		select {
		case <-time.After(150 * time.Millisecond):
			return "survived", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	callCtx, callCancel := context.WithCancel(context.Background())
	id, err := m.Launch(callCtx, runner, "worker", "slow task", "x", SubAgentParams{Role: "worker"}, 0)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	// Cancel the foreground/call context immediately — the job must
	// keep running.
	callCancel()

	waitForJob(t, m, id, 2*time.Second)

	if !completed.Load() {
		t.Fatal("background job did not complete; it was likely cancelled by the call context")
	}
	st, _ := m.Status(id)
	if st.Status != BGStatusCompleted {
		t.Errorf("status = %q, want completed (job should survive call-context cancellation)", st.Status)
	}
	if st.Error != "" {
		t.Errorf("unexpected error: %s", st.Error)
	}
}

// TestBackgroundJobs_TimeoutBounds verifies the job's own timeout still
// applies and is independent of the call context.
func TestBackgroundJobs_TimeoutBounds(t *testing.T) {
	m := newTestManager()
	defer m.Close()

	var errMu sync.Mutex
	var gotErr error
	m.Deliver = func(role, desc, res string, err error) {
		errMu.Lock()
		gotErr = err
		errMu.Unlock()
	}

	runner := func(ctx context.Context, prompt string, p SubAgentParams) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}

	id, err := m.Launch(context.Background(), runner, "worker", "hang", "x", SubAgentParams{Role: "worker"}, 80*time.Millisecond)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	waitForJob(t, m, id, 2*time.Second)

	// Deliver runs after the finished flag flips; drain it before asserting.
	deadline := time.Now().Add(2 * time.Second)
	errMu.Lock()
	for gotErr == nil && time.Now().Before(deadline) {
		errMu.Unlock()
		time.Sleep(2 * time.Millisecond)
		errMu.Lock()
	}
	stillNil := gotErr == nil
	errMu.Unlock()
	if stillNil {
		t.Error("expected a timeout error, got nil")
	}
}

// TestBackgroundJobs_Cancel verifies explicit cancellation via Cancel.
func TestBackgroundJobs_Cancel(t *testing.T) {
	m := newTestManager()
	defer m.Close()

	runner := func(ctx context.Context, prompt string, p SubAgentParams) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}

	id, err := m.Launch(context.Background(), runner, "worker", "long", "x", SubAgentParams{Role: "worker"}, 0)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	if !m.Cancel(id) {
		t.Fatal("Cancel returned false for known job")
	}
	waitForJob(t, m, id, 2*time.Second)

	st, _ := m.Status(id)
	if st.Status != BGStatusCancelled {
		t.Errorf("status = %q, want cancelled", st.Status)
	}
}

// TestBackgroundJobs_OnUsage verifies usage is attributed on completion.
func TestBackgroundJobs_OnUsage(t *testing.T) {
	m := newTestManager()
	defer m.Close()

	var attributed types.Usage
	var mu sync.Mutex
	m.OnUsage = func(u types.Usage) {
		mu.Lock()
		attributed.PromptTokens += u.PromptTokens
		attributed.CompletionTokens += u.CompletionTokens
		attributed.TotalTokens += u.TotalTokens
		mu.Unlock()
	}

	runner := func(ctx context.Context, prompt string, p SubAgentParams) (string, error) {
		AddSubAgentUsage(ctx, types.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150})
		return "ok", nil
	}

	id, err := m.Launch(context.Background(), runner, "worker", "u", "x", SubAgentParams{Role: "worker"}, 0)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	waitForJob(t, m, id, 2*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if attributed.TotalTokens != 150 {
		t.Errorf("attributed total tokens = %d, want 150", attributed.TotalTokens)
	}
}

// TestBackgroundJobs_MaxConcurrent verifies the concurrency cap rejects
// excess launches.
func TestBackgroundJobs_MaxConcurrent(t *testing.T) {
	m := newTestManager()
	defer m.Close()
	m.MaxConcurrent = 1

	block := make(chan struct{})
	runner := func(ctx context.Context, prompt string, p SubAgentParams) (string, error) {
		<-block
		return "ok", nil
	}

	if _, err := m.Launch(context.Background(), runner, "worker", "a", "x", SubAgentParams{Role: "worker"}, 0); err != nil {
		t.Fatalf("first Launch: %v", err)
	}
	_, err := m.Launch(context.Background(), runner, "worker", "b", "x", SubAgentParams{Role: "worker"}, 0)
	if err == nil {
		t.Fatal("expected concurrency-limit error on second launch, got nil")
	}
	close(block)
}

// TestBackgroundJobs_CloseCancels verifies Close cancels in-flight jobs.
func TestBackgroundJobs_CloseCancels(t *testing.T) {
	m := newTestManager()

	cancelled := make(chan struct{})
	runner := func(ctx context.Context, prompt string, p SubAgentParams) (string, error) {
		<-ctx.Done()
		close(cancelled)
		return "", ctx.Err()
	}

	if _, err := m.Launch(context.Background(), runner, "worker", "long", "x", SubAgentParams{Role: "worker"}, 0); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	m.Close()

	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight job was not cancelled by Close")
	}
}

// TestBackgroundJobs_RunnerError verifies a runner error is reported as
// failed and delivered.
func TestBackgroundJobs_RunnerError(t *testing.T) {
	m := newTestManager()
	defer m.Close()

	var got jobResult
	var mu sync.Mutex
	m.Deliver = func(role, desc, res string, err error) {
		mu.Lock()
		got = jobResult{role, desc, res, err}
		mu.Unlock()
	}

	boom := errors.New("boom")
	runner := func(ctx context.Context, prompt string, p SubAgentParams) (string, error) {
		return "", boom
	}

	id, err := m.Launch(context.Background(), runner, "worker", "fail", "x", SubAgentParams{Role: "worker"}, 0)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	waitForJob(t, m, id, 2*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if !errors.Is(got.err, boom) {
		t.Errorf("delivered err = %v, want boom", got.err)
	}
	st, _ := m.Status(id)
	if st.Status != BGStatusFailed {
		t.Errorf("status = %q, want failed", st.Status)
	}
}

// TestBackgroundJobs_List verifies List returns snapshots for multiple
// jobs.
func TestBackgroundJobs_List(t *testing.T) {
	m := newTestManager()
	defer m.Close()

	var wg sync.WaitGroup
	runner := func(ctx context.Context, prompt string, p SubAgentParams) (string, error) {
		defer wg.Done()
		return prompt, nil
	}
	for i := 0; i < 3; i++ {
		wg.Add(1)
		if _, err := m.Launch(context.Background(), runner, "worker", "t", "x", SubAgentParams{Role: "worker"}, 0); err != nil {
			t.Fatalf("Launch %d: %v", i, err)
		}
	}
	wg.Wait()

	list := m.List()
	if len(list) != 3 {
		t.Errorf("List returned %d jobs, want 3", len(list))
	}
	for _, st := range list {
		if st.Status != BGStatusCompleted {
			t.Errorf("job %s status = %q, want completed", st.ID, st.Status)
		}
	}
}

func waitForJob(t *testing.T, m *BackgroundJobs, id string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, ok := m.Status(id)
		if ok && st.Status != BGStatusRunning {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish within %v", id, timeout)
}
