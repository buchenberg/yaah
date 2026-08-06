package process

import (
	"fmt"
	"testing"
	"time"
)

// ---------- NewManager ----------

func TestNewManager(t *testing.T) {
	m := NewManager()
	if m == nil {
		t.Fatal("NewManager returned nil")
	}
	if len(m.List()) != 0 {
		t.Error("new manager should have empty process list")
	}
}

// ---------- Start, Get, List ----------

func TestStartSimpleCommand(t *testing.T) {
	m := NewManager()

	info, err := m.Start("Write-Host hello", "test process")
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if info == nil {
		t.Fatal("Start returned nil info")
	}
	if info.ID == "" {
		t.Error("info.ID should not be empty")
	}
	if info.Status != "running" {
		t.Errorf("info.Status = %q; want %q", info.Status, "running")
	}
	if info.Command != "Write-Host hello" {
		t.Errorf("info.Command = %q; want %q", info.Command, "Write-Host hello")
	}
	if info.Description != "test process" {
		t.Errorf("info.Description = %q; want %q", info.Description, "test process")
	}

	// Verify it appears in List
	list := m.List()
	if len(list) != 1 {
		t.Fatalf("List length = %d; want 1", len(list))
	}
	if list[0].ID != info.ID {
		t.Errorf("List[0].ID = %q; want %q", list[0].ID, info.ID)
	}

	// Verify Get works
	got := m.Get(info.ID)
	if got == nil {
		t.Fatal("Get returned nil for existing process")
	}
	if got.ID != info.ID {
		t.Errorf("Get.ID = %q; want %q", got.ID, info.ID)
	}

	// Get non-existent
	if m.Get("nonexistent") != nil {
		t.Error("Get should return nil for non-existent process")
	}
}

func TestStartEchoProducesOutput(t *testing.T) {
	m := NewManager()

	info, err := m.Start("Write-Host 'line1'; Write-Host 'line2'", "echo test")
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for process to finish and logs to accumulate
	time.Sleep(500 * time.Millisecond)

	logs := info.Logs()
	if logs == "" {
		t.Error("Logs should not be empty after running echo")
	}
	t.Logf("logs: %q", logs)
}

func TestMultipleProcesses(t *testing.T) {
	m := NewManager()

	// Start several quick processes
	for i := 0; i < 3; i++ {
		if _, err := m.Start(fmt.Sprintf("Write-Host 'proc%d'", i),
			fmt.Sprintf("process %d", i)); err != nil {
			t.Fatalf("Start %d failed: %v", i, err)
		}
	}

	list := m.List()
	if len(list) != 3 {
		t.Errorf("List length = %d; want 3", len(list))
	}

	// Verify all IDs are unique
	seen := make(map[string]bool)
	for _, p := range list {
		if seen[p.ID] {
			t.Errorf("duplicate ID: %s", p.ID)
		}
		seen[p.ID] = true
	}
}

func TestStartFailingCommand(t *testing.T) {
	m := NewManager()

	info, err := m.Start("exit 1", "failing test")
	// Start may or may not return an error depending on shell
	_ = err

	// Wait for process to fail
	time.Sleep(time.Second)

	// The status should eventually be "error" since we exited with 1
	info.mu.Lock()
	status := info.Status
	info.mu.Unlock()

	t.Logf("failing command status: %q, logs: %q", status, info.Logs())
	// Status should be "error" (or at least not "running" anymore)
	if status == "running" {
		t.Error("failing command status still 'running' after wait")
	}
}

// ---------- Stop ----------

func TestStopRunningProcess(t *testing.T) {
	m := NewManager()

	// Start a long-running sleep
	info, err := m.Start("Start-Sleep -Seconds 30", "sleep test")
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Stop it
	err = m.Stop(info.ID)
	if err != nil {
		t.Errorf("Stop returned error: %v", err)
	}

	// Give it a moment to fully stop
	time.Sleep(200 * time.Millisecond)

	info.mu.Lock()
	status := info.Status
	info.mu.Unlock()
	if status != "stopped" {
		t.Errorf("status = %q; want %q", status, "stopped")
	}
}

func TestStopNonExistent(t *testing.T) {
	m := NewManager()
	err := m.Stop("nonexistent")
	if err == nil {
		t.Error("Stop should return error for non-existent process")
	}
}

func TestStopAlreadyStopped(t *testing.T) {
	m := NewManager()
	info, _ := m.Start("Start-Sleep -Seconds 5", "sleep test")

	_ = m.Stop(info.ID)
	time.Sleep(200 * time.Millisecond)

	// Second stop should fail
	err := m.Stop(info.ID)
	if err == nil {
		t.Error("second Stop should return error")
	}
}

// ---------- Logs ----------

func TestLogsInitiallyEmpty(t *testing.T) {
	m := NewManager()
	info, err := m.Start("Start-Sleep -Seconds 1", "logs test")
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	// Logs may be empty immediately (before output)
	logs := info.Logs()
	t.Logf("initial logs: %q", logs)
}
