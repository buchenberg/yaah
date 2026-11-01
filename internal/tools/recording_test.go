package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordingTestTool struct {
	name string
	fn   func(context.Context, string) (string, error)
}

func (t *recordingTestTool) Name() string            { return t.name }
func (t *recordingTestTool) Description() string     { return "test tool" }
func (t *recordingTestTool) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (t *recordingTestTool) Execute(ctx context.Context, args string) (string, error) {
	return t.fn(ctx, args)
}

func newRecordingTestTool(t *testing.T, name string, dir string, write func(path string)) (*RecordingTool, *ConflictTracker, *recordingTestTool) {
	t.Helper()
	tracker := &ConflictTracker{}
	inner := &recordingTestTool{name: name, fn: func(_ context.Context, _ string) (string, error) {
		write(filepath.Join(dir, "file.txt"))
		return "ok", nil
	}}
	pv := NewPathValidator(dir, false, nil)
	pv.WorkDir = pv.WorkspaceRoot
	rt := NewRecordingTool(inner, tracker)
	rt.SetPathValidator(pv)
	rt.SetWorkspace(newLocalWorkspace(pv))
	return rt, tracker, inner
}

func TestRecordingTool_FailedWriteNotRecorded(t *testing.T) {
	dir := t.TempDir()
	tracker := &ConflictTracker{}
	pv := NewPathValidator(dir, false, nil)
	pv.WorkDir = pv.WorkspaceRoot
	inner := &recordingTestTool{name: "write", fn: func(_ context.Context, _ string) (string, error) {
		_ = os.WriteFile(filepath.Join(dir, "file.txt"), []byte("partial\n"), 0o644)
		return "", errors.New("write failed")
	}}
	rt := NewRecordingTool(inner, tracker)
	rt.SetPathValidator(pv)
	rt.SetWorkspace(newLocalWorkspace(pv))

	if _, err := rt.Execute(WithConflictDispatch(context.Background(), "worker", ConflictLaneShared), `{"filePath":"file.txt"}`); err == nil {
		t.Fatal("expected the fake write to fail")
	}
	if got := tracker.DetectAndReset(); got != "" {
		t.Fatalf("a failed write must not be evidence of a conflict, got:\n%s", got)
	}
}

func TestRecordingTool_ForwardsDangerClassifier(t *testing.T) {
	rt := NewRecordingTool(&BashTool{}, nil)
	if !rt.IsDangerous(`{"command":"echo hi"}`) {
		t.Fatal("wrapping bash must not hide its approval classification")
	}
}

func TestConflictDispatch_IDUniqueAndLaneStable(t *testing.T) {
	first, ok := conflictDispatchFrom(WithConflictDispatch(context.Background(), "worker", ConflictLaneRetained))
	if !ok {
		t.Fatal("dispatch context missing")
	}
	second, ok := conflictDispatchFrom(WithConflictDispatch(context.Background(), "worker", ConflictLaneRetained))
	if !ok {
		t.Fatal("second dispatch context missing")
	}
	if first.ID == second.ID {
		t.Fatalf("dispatch IDs must be unique across calls, both = %q", first.ID)
	}
	if first.Label != "worker" || first.Lane != ConflictLaneRetained {
		t.Errorf("dispatch = %+v, want worker/retained", first)
	}
}

func TestRecordingTool_SameContentIsNotConflict(t *testing.T) {
	dir := t.TempDir()
	rt, tracker, _ := newRecordingTestTool(t, "write", dir, func(path string) {
		_ = os.WriteFile(path, []byte("same\n"), 0o644)
	})
	for _, label := range []string{"worker a", "worker b"} {
		if _, err := rt.Execute(WithConflictDispatch(context.Background(), label, ConflictLaneShared), `{"filePath":"./file.txt"}`); err != nil {
			t.Fatalf("%s write: %v", label, err)
		}
	}
	if got := tracker.DetectAndReset(); got != "" {
		t.Fatalf("identical writes must not conflict, got:\n%s", got)
	}
}

func TestRecordingTool_DivergentWritesConflictAndNormalize(t *testing.T) {
	dir := t.TempDir()
	tracker := &ConflictTracker{}
	calls := 0
	inner := &recordingTestTool{name: "write", fn: func(_ context.Context, _ string) (string, error) {
		calls++
		body := "one\n"
		if calls == 2 {
			body = "two\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte(body), 0o644); err != nil {
			return "", err
		}
		return "ok", nil
	}}
	pv := NewPathValidator(dir, false, nil)
	pv.WorkDir = pv.WorkspaceRoot
	rt := NewRecordingTool(inner, tracker)
	rt.SetPathValidator(pv)
	rt.SetWorkspace(newLocalWorkspace(pv))

	if _, err := rt.Execute(WithConflictDispatch(context.Background(), "worker a", ConflictLaneShared), `{"filePath":"./file.txt"}`); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := rt.Execute(WithConflictDispatch(context.Background(), "worker b", ConflictLaneShared), `{"filePath":"file.txt"}`); err != nil {
		t.Fatalf("second write: %v", err)
	}
	report := tracker.DetectAndResetReport()
	if report == nil || len(report.Entries) != 1 {
		t.Fatalf("report = %+v, want one conflict", report)
	}
	if report.Entries[0].Path != "file.txt" || report.Entries[0].Kind != ConflictKindShared {
		t.Errorf("entry = %+v, want normalized shared file.txt", report.Entries[0])
	}
}

func TestRecordingTool_ShellMutationIsUnknown(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{"redirect", "echo hi > out.txt", true},
		{"common mutator", "make test", true},
		{"separator mutator", "make&&echo ok", true},
		{"nested interpreter", `bash -lc "sed -i s/a/b/ file"`, true},
		{"quoted redirect", "git log --format='%h > %s'", false},
		{"escaped redirect", `echo \> out.txt`, false},
		{"read only", "git status", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tracker := &ConflictTracker{}
			inner := &recordingTestTool{name: "bash", fn: func(context.Context, string) (string, error) {
				return "ok", nil
			}}
			rt := NewRecordingTool(inner, tracker)
			if _, err := rt.Execute(WithConflictDispatch(context.Background(), "worker", ConflictLaneShared), `{"command":`+jsonString(tc.command)+`}`); err != nil {
				t.Fatalf("bash: %v", err)
			}
			report := tracker.DetectAndResetReport()
			if tc.want {
				if report == nil || len(report.Unknown) != 1 {
					t.Fatalf("report = %+v, want an unknown shell mutation", report)
				}
				if got := report.Render(); !strings.Contains(got, "UNKNOWN MUTATIONS") {
					t.Errorf("render = %q, want unknown mutation warning", got)
				}
				return
			}
			if report != nil {
				t.Fatalf("read-only command produced a report: %+v", report)
			}
		})
	}
}

func TestConflictSnapshotsEqual_UnknownShape(t *testing.T) {
	a := conflictSnapshot{Exists: true, Size: 100, Mode: "-rw-r--r--"}
	b := conflictSnapshot{Exists: true, Size: 100, Mode: "-rw-r--r--"}
	if !conflictSnapshotsEqual(a, b) {
		t.Error("same unknown shape should suppress an idempotent rewrite")
	}
	b.Size = 101
	if conflictSnapshotsEqual(a, b) {
		t.Error("different unknown size should remain evidence of a change")
	}
}

func TestRecordingTool_FailedShellStillUnknown(t *testing.T) {
	tracker := &ConflictTracker{}
	inner := &recordingTestTool{name: "bash", fn: func(context.Context, string) (string, error) {
		return "", errors.New("exit status 1 after writing")
	}}
	rt := NewRecordingTool(inner, tracker)
	if _, err := rt.Execute(WithConflictDispatch(context.Background(), "worker", ConflictLaneShared), `{"command":"make test"}`); err == nil {
		t.Fatal("expected the fake shell to fail")
	}
	report := tracker.DetectAndResetReport()
	if report == nil || len(report.Unknown) != 1 {
		t.Fatalf("report = %+v, want an unknown mutation from the failed command", report)
	}
}

func TestRecordingTool_RetainedLaneIsSpeculative(t *testing.T) {
	dir := t.TempDir()
	tracker := &ConflictTracker{}
	calls := 0
	inner := &recordingTestTool{name: "write", fn: func(_ context.Context, _ string) (string, error) {
		calls++
		body := "one\n"
		if calls == 2 {
			body = "two\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte(body), 0o644); err != nil {
			return "", err
		}
		return "ok", nil
	}}
	pv := NewPathValidator(dir, false, nil)
	pv.WorkDir = pv.WorkspaceRoot
	rt := NewRecordingTool(inner, tracker)
	rt.SetPathValidator(pv)
	rt.SetWorkspace(newLocalWorkspace(pv))

	for _, label := range []string{"worker a", "worker b"} {
		if _, err := rt.Execute(WithConflictDispatch(context.Background(), label, ConflictLaneRetained), `{"filePath":"file.txt"}`); err != nil {
			t.Fatalf("%s write: %v", label, err)
		}
	}
	report := tracker.DetectAndResetReport()
	if report == nil || len(report.Entries) != 1 || report.Entries[0].Kind != ConflictKindRetained {
		t.Fatalf("report = %+v, want retained speculative overlap", report)
	}
	if got := report.Render(); !strings.Contains(got, "SPECULATIVE OVERLAP") {
		t.Errorf("render = %q, want speculative warning", got)
	}
}
