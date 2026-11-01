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

func TestConflictTracker_MixedRoleLabels(t *testing.T) {
	ct := &ConflictTracker{}
	ct.Record("analyst — list files", "/a.go", "write")
	ct.Record("worker — implement", "/a.go", "edit")

	report := ct.DetectAndReset()
	if report == "" {
		t.Fatal("expected conflict report for mixed role labels, got empty")
	}
	if !strings.Contains(report, "analyst — list files") {
		t.Errorf("report should contain the first role label, got: %s", report)
	}
}

func TestConflictTracker_IdenticalEvidenceIsNotConflict(t *testing.T) {
	ct := &ConflictTracker{}
	ct.RecordChange(FileRecord{
		DispatchID: "a", SubAgentLabel: "worker a", Lane: ConflictLaneShared,
		FilePath: "src/a.go", ToolName: "write", Known: true,
		BeforeExists: true, AfterExists: true, BeforeDigest: "before", AfterDigest: "same", AfterMode: "-rw-r--r--",
	})
	ct.RecordChange(FileRecord{
		DispatchID: "b", SubAgentLabel: "worker b", Lane: ConflictLaneShared,
		FilePath: "src/a.go", ToolName: "write", Known: true,
		BeforeExists: true, AfterExists: true, BeforeDigest: "before", AfterDigest: "same", AfterMode: "-rw-r--r--",
	})
	if got := ct.DetectAndReset(); got != "" {
		t.Fatalf("identical writes must not conflict, got:\n%s", got)
	}
}

func TestConflictTracker_DivergentEvidenceConflict(t *testing.T) {
	ct := &ConflictTracker{}
	for _, rec := range []FileRecord{
		{DispatchID: "a", SubAgentLabel: "worker a", FilePath: "src/./a.go", ToolName: "write", Known: true, AfterExists: true, AfterDigest: "one", AfterMode: "-rw-r--r--"},
		{DispatchID: "b", SubAgentLabel: "worker b", FilePath: "src/a.go", ToolName: "edit", Known: true, AfterExists: true, AfterDigest: "two", AfterMode: "-rw-r--r--"},
	} {
		ct.RecordChange(rec)
	}
	report := ct.DetectAndResetReport()
	if report == nil || len(report.Entries) != 1 {
		t.Fatalf("report = %+v, want one entry", report)
	}
	entry := report.Entries[0]
	if entry.Kind != ConflictKindShared || entry.Path != "src/a.go" {
		t.Errorf("entry = %+v, want shared src/a.go", entry)
	}
	if got := report.Render(); !strings.Contains(got, "CONFLICT:") || !strings.Contains(got, "src/a.go") {
		t.Errorf("render = %q, want shared conflict for src/a.go", got)
	}
}

func TestConflictTracker_RetainedOverlapIsSpeculative(t *testing.T) {
	ct := &ConflictTracker{}
	for _, rec := range []FileRecord{
		{DispatchID: "a", SubAgentLabel: "worker a", Lane: ConflictLaneRetained, FilePath: "out.txt", ToolName: "write", Known: true, AfterExists: true, AfterDigest: "one", AfterMode: "-rw-r--r--"},
		{DispatchID: "b", SubAgentLabel: "worker b", Lane: ConflictLaneRetained, FilePath: "out.txt", ToolName: "write", Known: true, AfterExists: true, AfterDigest: "two", AfterMode: "-rw-r--r--"},
	} {
		ct.RecordChange(rec)
	}
	report := ct.DetectAndResetReport()
	if report == nil || len(report.Entries) != 1 || report.Entries[0].Kind != ConflictKindRetained {
		t.Fatalf("report = %+v, want retained speculative overlap", report)
	}
	if got := report.Render(); !strings.Contains(got, "SPECULATIVE OVERLAP") || strings.Contains(got, "CONFLICT:") {
		t.Errorf("render = %q, want only a speculative warning", got)
	}
}

func TestConflictTracker_UnknownShellMutationIsReported(t *testing.T) {
	ct := &ConflictTracker{}
	ct.RecordUnknown(ConflictDispatch{ID: "a", Label: "worker a", Lane: ConflictLaneShared}, "bash", "may mutate: echo hi > out.txt")
	report := ct.DetectAndResetReport()
	if report == nil || len(report.Unknown) != 1 {
		t.Fatalf("report = %+v, want one unknown mutation", report)
	}
	if got := report.Render(); !strings.Contains(got, "UNKNOWN MUTATIONS") || !strings.Contains(got, "worker a") {
		t.Errorf("render = %q, want unknown mutation warning", got)
	}
}
