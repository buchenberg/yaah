package tools

import (
	"context"
	"encoding/json"
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
