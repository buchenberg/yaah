package tools

import (
	"strings"
	"testing"
)

func TestConflictTracker_NoConflicts(t *testing.T) {
	ct := &ConflictTracker{}
	ct.Record("worker — Add login", "/foo/bar.go", "write")
	ct.Record("worker — Add login", "/foo/bar.go", "edit")

	report := ct.DetectAndReset()
	if report != "" {
		t.Errorf("expected no conflict report, got: %s", report)
	}
}

func TestConflictTracker_Conflict(t *testing.T) {
	ct := &ConflictTracker{}
	ct.Record("worker — Add login", "/foo/bar.go", "write")
	ct.Record("worker — Refactor auth", "/foo/bar.go", "edit")

	report := ct.DetectAndReset()
	if report == "" {
		t.Fatal("expected conflict report, got empty")
	}

	if !strings.Contains(report, "/foo/bar.go") {
		t.Errorf("report should contain file path, got: %s", report)
	}
	if !strings.Contains(report, "worker — Add login") {
		t.Errorf("report should contain first sub-agent label, got: %s", report)
	}
	if !strings.Contains(report, "worker — Refactor auth") {
		t.Errorf("report should contain second sub-agent label, got: %s", report)
	}
}

func TestConflictTracker_MultipleFiles(t *testing.T) {
	ct := &ConflictTracker{}
	ct.Record("w1", "/a.go", "write")
	ct.Record("w2", "/a.go", "edit")
	ct.Record("w1", "/b.go", "write")
	ct.Record("w2", "/b.go", "write")

	report := ct.DetectAndReset()
	if report == "" {
		t.Fatal("expected conflict report, got empty")
	}

	if !strings.Contains(report, "/a.go") {
		t.Errorf("report should contain /a.go, got: %s", report)
	}
	if !strings.Contains(report, "/b.go") {
		t.Errorf("report should contain /b.go, got: %s", report)
	}
}

func TestConflictTracker_ResetsAfterDetect(t *testing.T) {
	ct := &ConflictTracker{}
	ct.Record("w1", "/a.go", "write")
	ct.Record("w2", "/a.go", "edit")

	_ = ct.DetectAndReset()

	report := ct.DetectAndReset()
	if report != "" {
		t.Errorf("tracker should be empty after reset, got: %s", report)
	}
}

func TestConflictTracker_DeduplicatesTools(t *testing.T) {
	ct := &ConflictTracker{}
	ct.Record("w1", "/a.go", "edit")
	ct.Record("w1", "/a.go", "edit")

	report := ct.DetectAndReset()
	if report != "" {
		t.Errorf("same sub-agent editing twice is not a conflict: %s", report)
	}
}

func TestConflictTracker_DefaultRoleLabel(t *testing.T) {
	ct := &ConflictTracker{}
	ct.Record("default — list files", "/a.go", "write")
	ct.Record("worker — implement", "/a.go", "edit")

	report := ct.DetectAndReset()
	if report == "" {
		t.Fatal("expected conflict report for mixed role labels, got empty")
	}
	if !strings.Contains(report, "default — list files") {
		t.Errorf("report should contain default role label, got: %s", report)
	}
}
