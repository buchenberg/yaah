package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/jobs"
)

// TestSubAgentJobs_StatusReportsStillRunning pins the yaah-37s honesty fix:
// status must expose still_running rather than letting a single status string
// imply a terminal state that has not happened yet.
func TestSubAgentJobs_StatusReportsStillRunning(t *testing.T) {
	mgr := jobs.NewBackgroundJobs()
	defer mgr.CloseAndWait(5 * time.Second)

	release := make(chan struct{})
	runner := func(context.Context, string, jobs.SubAgentParams) (string, error) {
		<-release
		return "done", nil
	}

	id, err := mgr.Launch(context.Background(), runner, "checker", "blocking job", "go", jobs.SubAgentParams{}, 10*time.Second)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	tool := &SubAgentJobsTool{Jobs: mgr}
	out, err := tool.executeStatus(id)
	if err != nil {
		t.Fatalf("executeStatus: %v", err)
	}

	var st struct {
		Status       string `json:"status"`
		StillRunning bool   `json:"still_running"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if st.Status != jobs.BGStatusRunning {
		t.Errorf("status = %q, want %q", st.Status, jobs.BGStatusRunning)
	}
	if !st.StillRunning {
		t.Errorf("still_running = false, want true for a running job")
	}

	close(release)
	mgr.CloseAndWait(5 * time.Second)
}

// TestSubAgentJobs_WaitTimeoutReportsBothFacts pins the other half of
// yaah-37s: a wait that hits its deadline must say the job is still running
// AND that the wait timed out, instead of rewriting status to "timed_out".
func TestSubAgentJobs_WaitTimeoutReportsBothFacts(t *testing.T) {
	mgr := jobs.NewBackgroundJobs()
	defer mgr.CloseAndWait(5 * time.Second)

	release := make(chan struct{})
	runner := func(context.Context, string, jobs.SubAgentParams) (string, error) {
		<-release
		return "done", nil
	}

	id, err := mgr.Launch(context.Background(), runner, "checker", "blocking job", "go", jobs.SubAgentParams{}, 10*time.Second)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	tool := &SubAgentJobsTool{Jobs: mgr}
	out, err := tool.executeWait(context.Background(), id, 1)
	if err != nil {
		t.Fatalf("executeWait: %v", err)
	}

	var w struct {
		Status       string `json:"status"`
		StillRunning bool   `json:"still_running"`
		TimedOut     bool   `json:"timed_out"`
	}
	if err := json.Unmarshal([]byte(out), &w); err != nil {
		t.Fatalf("unmarshal wait: %v", err)
	}
	if w.Status != jobs.BGStatusRunning {
		t.Errorf("status = %q, want %q (must not be rewritten to timed_out)", w.Status, jobs.BGStatusRunning)
	}
	if !w.StillRunning {
		t.Errorf("still_running = false, want true")
	}
	if !w.TimedOut {
		t.Errorf("timed_out = false, want true")
	}

	close(release)
	mgr.CloseAndWait(5 * time.Second)
}

// TestSubAgentJobs_CancelDoesNotFabricateTerminalState pins the cancel
// honesty fix: a cancel that has not yet been observed must keep the true
// running status with still_running, not fabricate "cancelled".
func TestSubAgentJobs_CancelDoesNotFabricateTerminalState(t *testing.T) {
	mgr := jobs.NewBackgroundJobs()
	defer mgr.CloseAndWait(5 * time.Second)

	release := make(chan struct{})
	runner := func(context.Context, string, jobs.SubAgentParams) (string, error) {
		<-release
		return "done", nil
	}

	id, err := mgr.Launch(context.Background(), runner, "checker", "blocking job", "go", jobs.SubAgentParams{}, 10*time.Second)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	tool := &SubAgentJobsTool{Jobs: mgr}
	out, err := tool.executeCancel(id)
	if err != nil {
		t.Fatalf("executeCancel: %v", err)
	}

	var st struct {
		Status       string `json:"status"`
		StillRunning bool   `json:"still_running"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("unmarshal cancel: %v", err)
	}
	if st.Status != jobs.BGStatusRunning {
		t.Errorf("status = %q, want %q (cancel must not fabricate 'cancelled' while still running)", st.Status, jobs.BGStatusRunning)
	}
	if !st.StillRunning {
		t.Errorf("still_running = false, want true")
	}

	close(release)
	mgr.CloseAndWait(5 * time.Second)
}

// TestSubAgentJobs_WaitHonoursCallerContext pins yaah-qwo: wait derives its
// deadline from the caller's context, so a cancelled turn, a parent deadline
// or a teardown interrupts the wait immediately instead of blocking for the
// full timeout_seconds. The result must say the wait was interrupted — never
// that the job timed out or finished.
func TestSubAgentJobs_WaitHonoursCallerContext(t *testing.T) {
	tests := []struct {
		name            string
		timeoutSecs     int
		cancelAfter     time.Duration // 0 = leave the parent context alone
		wantMax         time.Duration
		wantInterrupted bool
		wantTimedOut    bool
	}{
		{
			// The schema maximum: the pre-fix code spun this long even
			// though the turn had already been aborted.
			name:            "parent cancel returns promptly",
			timeoutSecs:     300,
			cancelAfter:     50 * time.Millisecond,
			wantMax:         5 * time.Second,
			wantInterrupted: true,
			wantTimedOut:    false,
		},
		{
			name:            "own deadline still reports a timeout",
			timeoutSecs:     1,
			wantMax:         5 * time.Second,
			wantInterrupted: false,
			wantTimedOut:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr := jobs.NewBackgroundJobs()
			defer mgr.CloseAndWait(5 * time.Second)

			release := make(chan struct{})
			runner := func(context.Context, string, jobs.SubAgentParams) (string, error) {
				<-release
				return "done", nil
			}
			id, err := mgr.Launch(context.Background(), runner, "checker", "blocking job", "go", jobs.SubAgentParams{}, 10*time.Minute)
			if err != nil {
				t.Fatalf("Launch: %v", err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelAfter > 0 {
				time.AfterFunc(tc.cancelAfter, cancel)
			}

			tool := &SubAgentJobsTool{Jobs: mgr}
			start := time.Now()
			out, err := tool.executeWait(ctx, id, tc.timeoutSecs)
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("executeWait: %v", err)
			}
			if elapsed > tc.wantMax {
				t.Errorf("executeWait took %v, want <= %v — wait must honour the caller context", elapsed, tc.wantMax)
			}

			var got struct {
				Status       string `json:"status"`
				StillRunning bool   `json:"still_running"`
				TimedOut     bool   `json:"timed_out"`
				Interrupted  bool   `json:"interrupted"`
			}
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("unmarshal wait: %v", err)
			}
			if got.Interrupted != tc.wantInterrupted {
				t.Errorf("interrupted = %v, want %v (raw: %s)", got.Interrupted, tc.wantInterrupted, out)
			}
			if got.TimedOut != tc.wantTimedOut {
				t.Errorf("timed_out = %v, want %v (raw: %s)", got.TimedOut, tc.wantTimedOut, out)
			}
			if got.Status != jobs.BGStatusRunning {
				t.Errorf("status = %q, want %q — a cut-short wait must not report a terminal state", got.Status, jobs.BGStatusRunning)
			}
			if !got.StillRunning {
				t.Errorf("still_running = false, want true for an unfinished job (raw: %s)", out)
			}

			close(release)
			mgr.CloseAndWait(5 * time.Second)
		})
	}
}

// TestSubAgentJobs_CancelOutcomes pins yaah-atw: cancel reports three
// distinct outcomes — cancelled, already_finished, unknown — instead of
// force-labelling every known id "cancelled" and marshalling a zero-value
// snapshot for a reaped one.
func TestSubAgentJobs_CancelOutcomes(t *testing.T) {
	type got struct {
		Cancel       string `json:"cancel"`
		Status       string `json:"status"`
		StillRunning bool   `json:"still_running"`
	}

	t.Run("running job reports cancelled", func(t *testing.T) {
		mgr := jobs.NewBackgroundJobs()
		defer mgr.CloseAndWait(5 * time.Second)

		release := make(chan struct{})
		runner := func(context.Context, string, jobs.SubAgentParams) (string, error) {
			<-release
			return "done", nil
		}
		id, err := mgr.Launch(context.Background(), runner, "checker", "blocking job", "go", jobs.SubAgentParams{}, 10*time.Minute)
		if err != nil {
			t.Fatalf("Launch: %v", err)
		}

		tool := &SubAgentJobsTool{Jobs: mgr}
		out, err := tool.executeCancel(id)
		if err != nil {
			t.Fatalf("executeCancel: %v", err)
		}
		var res got
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatalf("unmarshal cancel: %v", err)
		}
		if res.Cancel != cancelCancelled {
			t.Errorf("cancel = %q, want %q (raw: %s)", res.Cancel, cancelCancelled, out)
		}
		if res.Status != jobs.BGStatusRunning || !res.StillRunning {
			t.Errorf("status = %q still_running = %v, want %q/true — a request is not a stop", res.Status, res.StillRunning, jobs.BGStatusRunning)
		}

		close(release)
		mgr.CloseAndWait(5 * time.Second)
	})

	t.Run("finished job reports already_finished", func(t *testing.T) {
		mgr := jobs.NewBackgroundJobs()
		defer mgr.CloseAndWait(5 * time.Second)

		runner := func(context.Context, string, jobs.SubAgentParams) (string, error) {
			return "done", nil
		}
		id, err := mgr.Launch(context.Background(), runner, "checker", "quick job", "go", jobs.SubAgentParams{}, 10*time.Minute)
		if err != nil {
			t.Fatalf("Launch: %v", err)
		}
		waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		st, ok := mgr.Wait(waitCtx, id)
		if !ok || st.StillRunning {
			t.Fatalf("job did not finish: ok=%v still_running=%v", ok, st.StillRunning)
		}

		tool := &SubAgentJobsTool{Jobs: mgr}
		out, err := tool.executeCancel(id)
		if err != nil {
			t.Fatalf("executeCancel: %v", err)
		}
		var res got
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatalf("unmarshal cancel: %v", err)
		}
		if res.Cancel != cancelAlreadyFinished {
			t.Errorf("cancel = %q, want %q — cancelling a finished job is not a cancellation (raw: %s)", res.Cancel, cancelAlreadyFinished, out)
		}
		if res.Status != jobs.BGStatusCompleted {
			t.Errorf("status = %q, want %q (the real final state)", res.Status, jobs.BGStatusCompleted)
		}
		if res.StillRunning {
			t.Errorf("still_running = true, want false for a finished job")
		}
	})

	t.Run("reaped or absent id reports unknown", func(t *testing.T) {
		mgr := jobs.NewBackgroundJobs()
		defer mgr.CloseAndWait(5 * time.Second)

		tool := &SubAgentJobsTool{Jobs: mgr}
		// An id that was never dispatched stands in for a reaped record:
		// both are ids the manager no longer knows.
		out, err := tool.executeCancel("bg-999")
		if err != nil {
			t.Fatalf("executeCancel: %v", err)
		}
		var res got
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatalf("unmarshal cancel: %v", err)
		}
		if res.Cancel != cancelUnknown {
			t.Errorf("cancel = %q, want %q (raw: %s)", res.Cancel, cancelUnknown, out)
		}
		if strings.Contains(out, `"status"`) {
			t.Errorf("a zero-value job snapshot must not be marshalled for an unknown id: %s", out)
		}
		if res.StillRunning {
			t.Errorf("still_running = true, want false for an unknown id")
		}
	})
}

// TestClampWaitTimeout pins the schema's 1-300 window for wait: bounds are
// advisory to a caller, and an unclamped huge value overflows the Duration
// multiply into an already-expired context — an instant, lying timed_out.
// MaxInt64 nanoseconds is about 9.223372036854e9 seconds, so 9_223_372_037
// genuinely wraps.
func TestClampWaitTimeout(t *testing.T) {
	if got := clampWaitTimeout(9_223_372_037); got != 300 {
		t.Errorf("clampWaitTimeout(overflow-scale value) = %d, want 300", got)
	}
	if got := clampWaitTimeout(301); got != 300 {
		t.Errorf("clampWaitTimeout(301) = %d, want 300", got)
	}
	if got := clampWaitTimeout(300); got != 300 {
		t.Errorf("clampWaitTimeout(300) = %d, want 300", got)
	}
	if got := clampWaitTimeout(1); got != 1 {
		t.Errorf("clampWaitTimeout(1) = %d, want 1", got)
	}
	if got := clampWaitTimeout(0); got != 0 {
		t.Errorf("clampWaitTimeout(0) = %d, want 0 (the default mapping is executeWait's job)", got)
	}
}
