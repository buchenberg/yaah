package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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

// partialMoveWorkspace turns a successful Copy into the cross-filesystem
// partial outcome: dst exists, src remains, and Move reports the typed error.
type partialMoveWorkspace struct {
	Workspace
}

func (w *partialMoveWorkspace) Move(ctx context.Context, src, dst string) error {
	if err := w.Workspace.Copy(ctx, src, dst); err != nil {
		return err
	}
	return &partialMoveError{Src: src, Dst: dst, Err: errors.New("permission denied")}
}

// TestRecordingTool_PartialMoveRecordsDestination pins the cross-filesystem
// move edge: Copy created dst but Remove of src failed. The call errors, yet the
// destination change is real and must be recorded — otherwise a parallel writer
// sees no evidence and a retry without force is refused because dst now exists.
//
// It drives the real MoveTool so the test also covers Execute's `%w` wrapping:
// if that boundary ever dropped the wrap, errors.As in recordTransfer (and here)
// would stop matching.
func TestRecordingTool_PartialMoveRecordsDestination(t *testing.T) {
	dir := t.TempDir()
	tracker := &ConflictTracker{}
	pv := NewPathValidator(dir, false, nil)
	pv.WorkDir = pv.WorkspaceRoot
	src := filepath.Join(dir, "src.txt")
	writeFile(t, src, []byte("payload"))

	ws := &partialMoveWorkspace{Workspace: newLocalWorkspace(pv)}
	rt := NewRecordingTool(&MoveTool{}, tracker)
	rt.SetPathValidator(pv)
	rt.SetWorkspace(ws)

	_, err := rt.Execute(WithConflictDispatch(context.Background(), "worker", ConflictLaneShared),
		`{"path":"src.txt","to":"dst.txt"}`)
	var partial *partialMoveError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %v, want the MoveTool wrap to preserve *partialMoveError", err)
	}

	tracker.mu.Lock()
	records := append([]FileRecord(nil), tracker.records...)
	tracker.mu.Unlock()
	if len(records) != 1 {
		t.Fatalf("records = %d, want only the created destination: %+v", len(records), records)
	}
	if records[0].FilePath != "dst.txt" || !records[0].AfterExists {
		t.Errorf("record = %+v, want the created destination dst.txt", records[0])
	}
}

// TestRecordingTool_RecursiveRmdirRecordsUnknown pins the no-false-all-clear
// rule for the most destructive new call: a recursive rmdir cannot enumerate
// the files it removed without walking the tree first, so it must record an
// explicit unknown. A parallel worker editing a file inside the tree then sees
// evidence instead of "nothing changed".
func TestRecordingTool_RecursiveRmdirRecordsUnknown(t *testing.T) {
	tracker := &ConflictTracker{}
	inner := &recordingTestTool{name: "rmdir", fn: func(context.Context, string) (string, error) {
		return "Removed directory tree /workspace/tree", nil
	}}
	rt := NewRecordingTool(inner, tracker)

	if _, err := rt.Execute(WithConflictDispatch(context.Background(), "worker", ConflictLaneShared),
		`{"path":"/workspace/tree","recursive":true}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	report := tracker.DetectAndResetReport()
	if report == nil || len(report.Unknown) != 1 {
		t.Fatalf("report = %+v, want one unknown entry", report)
	}
	if !strings.Contains(report.Unknown[0].Detail, "rmdir -r /workspace/tree") {
		t.Errorf("unknown detail = %q, want the recursive path", report.Unknown[0].Detail)
	}

	// A failed recursive removal may still have deleted part of the tree, so it
	// records the unknown too, matching a failed shell command.
	inner.fn = func(context.Context, string) (string, error) {
		return "", errors.New("permission denied")
	}
	if _, err := rt.Execute(WithConflictDispatch(context.Background(), "worker", ConflictLaneShared),
		`{"path":"/workspace/tree","recursive":true}`); err == nil {
		t.Fatal("expected the fake rmdir to fail")
	}
	report = tracker.DetectAndResetReport()
	if report == nil || len(report.Unknown) != 1 {
		t.Fatalf("failed recursive rmdir report = %+v, want one unknown entry", report)
	}
	if !strings.Contains(report.Unknown[0].Detail, "reported an error") {
		t.Errorf("unknown detail = %q, want the failure noted", report.Unknown[0].Detail)
	}
}

// TestRecordingTool_EmptyRmdirNotRecorded pins the other half: removing an
// empty directory touches no file, so it is not conflict evidence.
func TestRecordingTool_EmptyRmdirNotRecorded(t *testing.T) {
	tracker := &ConflictTracker{}
	inner := &recordingTestTool{name: "rmdir", fn: func(context.Context, string) (string, error) {
		return "Removed directory /workspace/empty", nil
	}}
	rt := NewRecordingTool(inner, tracker)

	if _, err := rt.Execute(WithConflictDispatch(context.Background(), "worker", ConflictLaneShared),
		`{"path":"/workspace/empty"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if report := tracker.DetectAndResetReport(); report != nil {
		t.Fatalf("report = %+v, want no evidence for an empty rmdir", report)
	}
}

// TestRecordingTool_RecursiveRmdirDetailBounded pins the conflict-report
// hardening: a long or non-UTF-8 path must not reach FileRecord.Detail raw.
func TestRecordingTool_RecursiveRmdirDetailBounded(t *testing.T) {
	tracker := &ConflictTracker{}
	inner := &recordingTestTool{name: "rmdir", fn: func(context.Context, string) (string, error) {
		return "Removed directory tree", nil
	}}
	rt := NewRecordingTool(inner, tracker)

	longPath := "/workspace/" + strings.Repeat("a", 300)
	args, err := json.Marshal(map[string]any{"path": longPath, "recursive": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Execute(WithConflictDispatch(context.Background(), "worker", ConflictLaneShared), string(args)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	report := tracker.DetectAndResetReport()
	if report == nil || len(report.Unknown) != 1 {
		t.Fatalf("report = %+v, want one unknown entry", report)
	}
	detail := report.Unknown[0].Detail
	if n := utf8.RuneCountInString(detail); n > 120 {
		t.Errorf("detail length = %d runes, want <= 120: %q", n, detail)
	}
	if !strings.HasPrefix(detail, "rmdir -r ") {
		t.Errorf("detail = %q, want the rmdir -r prefix preserved", detail)
	}
}

func TestBoundedDetail_TruncatesAndSanitizes(t *testing.T) {
	long := boundedDetail(strings.Repeat("x", 300))
	if n := utf8.RuneCountInString(long); n != 120 {
		t.Errorf("truncated length = %d runes, want 120", n)
	}
	if !strings.HasSuffix(long, "...") {
		t.Errorf("truncated detail = %q, want a trailing ellipsis", long)
	}

	got := boundedDetail("ok\xffbad")
	if !utf8.ValidString(got) {
		t.Errorf("sanitized detail is not valid UTF-8: %q", got)
	}
	if got != "ok?bad" {
		t.Errorf("sanitized detail = %q, want invalid bytes replaced with '?'", got)
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

func TestConflictSnapshotsEqual_UnknownIsEvidence(t *testing.T) {
	a := conflictSnapshot{Exists: true, Mode: "-rw-r--r--"}
	b := conflictSnapshot{Exists: true, Mode: "-rw-r--r--"}
	if conflictSnapshotsEqual(a, b) {
		t.Error("content-unknown snapshots must remain unverified evidence, not a no-op")
	}
	if conflictSnapshotsEqual(conflictSnapshot{}, conflictSnapshot{}) {
		t.Error("unobservable existence must not compare equal to a no-op")
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
