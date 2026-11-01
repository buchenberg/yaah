package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync/atomic"
)

type contextKey string

const conflictLabelKey contextKey = "yaah-conflict-label"

// ConflictDispatch identifies one delegate call for conflict evidence. The ID
// is unique per dispatch; the label is the human-readable role/description.
type ConflictDispatch struct {
	ID    string
	Label string
	Lane  string
}

var conflictDispatchSeq atomic.Uint64

// WithConflictLabel retains the old label-only entry point. It is a shared-lane
// dispatch because callers that use it have no retained-world context.
func WithConflictLabel(ctx context.Context, label string) context.Context {
	return WithConflictDispatch(ctx, label, ConflictLaneShared)
}

// WithConflictDispatch attaches a unique dispatch identity and lane to the
// sub-agent context. A background job carries this context into its runner, so
// its records are attributed to the dispatching call rather than the turn that
// happens to observe them.
func WithConflictDispatch(ctx context.Context, label, lane string) context.Context {
	if lane == "" {
		lane = ConflictLaneShared
	}
	id := fmt.Sprintf("%s#%d", label, conflictDispatchSeq.Add(1))
	return context.WithValue(ctx, conflictLabelKey, ConflictDispatch{
		ID:    id,
		Label: label,
		Lane:  lane,
	})
}

func conflictDispatchFrom(ctx context.Context) (ConflictDispatch, bool) {
	if ctx == nil {
		return ConflictDispatch{}, false
	}
	d, ok := ctx.Value(conflictLabelKey).(ConflictDispatch)
	if !ok || d.Label == "" {
		return ConflictDispatch{}, false
	}
	if d.Lane == "" {
		d.Lane = ConflictLaneShared
	}
	return d, true
}

// RecordingTool records successful writes and shell mutations for the parent
// conflict tracker. It records after execution: failed or no-op writes are not
// evidence of a clobber.
type RecordingTool struct {
	inner   Tool
	tracker *ConflictTracker
	ws      Workspace
	pv      *PathValidator
}

func NewRecordingTool(inner Tool, tracker *ConflictTracker) *RecordingTool {
	return &RecordingTool{inner: inner, tracker: tracker}
}

func (rt *RecordingTool) Name() string            { return rt.inner.Name() }
func (rt *RecordingTool) Description() string     { return rt.inner.Description() }
func (rt *RecordingTool) Schema() json.RawMessage { return rt.inner.Schema() }

var (
	_ PathValidatorSetter = (*RecordingTool)(nil)
	_ WorkspaceSetter     = (*RecordingTool)(nil)
	_ DangerClassifier    = (*RecordingTool)(nil)
)

// IsDangerous forwards the wrapped tool's approval classification. Wrapping a
// destructive tool must not make it look safe to the gate.
func (rt *RecordingTool) IsDangerous(argsJSON string) bool {
	if dc, ok := rt.inner.(DangerClassifier); ok {
		return dc.IsDangerous(argsJSON)
	}
	return false
}

// SetPathValidator forwards the validator to the wrapped tool so
// registry auto-injection reaches tools registered through the wrapper, and
// keeps it for evidence snapshots on the local lane.
func (rt *RecordingTool) SetPathValidator(pv *PathValidator) {
	rt.pv = pv
	if setter, ok := rt.inner.(PathValidatorSetter); ok {
		setter.SetPathValidator(pv)
	}
}

// SetWorkspace forwards the workspace to the wrapped tool for the same reason.
// Registry classification unwraps this type before inspecting markers, so the
// wrapped tool is what the isolation gate judges.
func (rt *RecordingTool) SetWorkspace(ws Workspace) {
	rt.ws = ws
	if setter, ok := rt.inner.(WorkspaceSetter); ok {
		setter.SetWorkspace(ws)
	}
}

func (rt *RecordingTool) workspace() Workspace {
	if rt.ws != nil {
		return rt.ws
	}
	if rt.pv != nil {
		return newLocalWorkspace(rt.pv)
	}
	return nil
}

func (rt *RecordingTool) Execute(ctx context.Context, args string) (string, error) {
	dispatch, tracked := conflictDispatchFrom(ctx)
	if rt.tracker == nil || !tracked {
		return rt.inner.Execute(ctx, args)
	}

	toolName := rt.inner.Name()
	if toolName == "bash" || toolName == "powershell" {
		result, err := rt.inner.Execute(ctx, args)
		if err == nil && shellMayMutate(args) {
			rt.tracker.RecordUnknown(dispatch, toolName, shellDetail(args))
		}
		return result, err
	}

	var params struct {
		FilePath string `json:"filePath"`
	}
	if json.Unmarshal([]byte(args), &params) != nil || params.FilePath == "" {
		return rt.inner.Execute(ctx, args)
	}
	ws := rt.workspace()
	if ws == nil {
		return rt.inner.Execute(ctx, args)
	}
	abs, err := ws.ResolvePath(params.FilePath)
	if err != nil {
		return rt.inner.Execute(ctx, args)
	}

	before := captureConflictSnapshot(ctx, ws, abs)
	result, err := rt.inner.Execute(ctx, args)
	if err != nil {
		return result, err
	}
	after := captureConflictSnapshot(ctx, ws, abs)
	if conflictSnapshotsEqual(before, after) {
		return result, nil
	}
	rt.tracker.RecordChange(FileRecord{
		DispatchID:    dispatch.ID,
		SubAgentLabel: dispatch.Label,
		Lane:          dispatch.Lane,
		FilePath:      conflictWorkspaceRel(ws, abs),
		ToolName:      toolName,
		BeforeExists:  before.Exists,
		AfterExists:   after.Exists,
		BeforeDigest:  before.Digest,
		AfterDigest:   after.Digest,
		BeforeMode:    before.Mode,
		AfterMode:     after.Mode,
		Known:         before.Known && after.Known,
	})
	return result, nil
}

type conflictSnapshot struct {
	Exists bool
	Known  bool
	Digest string
	Mode   string
}

func captureConflictSnapshot(ctx context.Context, ws Workspace, abs string) conflictSnapshot {
	info, err := ws.Lstat(ctx, abs)
	if errors.Is(err, fs.ErrNotExist) {
		return conflictSnapshot{Known: true}
	}
	if err != nil {
		return conflictSnapshot{}
	}
	mode := info.Mode().String()
	if !info.Mode().IsRegular() || info.Size() > conflictTrackerMaxHashBytes {
		return conflictSnapshot{Exists: true, Mode: mode}
	}
	data, err := ws.ReadFile(ctx, abs)
	if err != nil {
		return conflictSnapshot{Exists: true, Mode: mode}
	}
	return conflictSnapshot{Exists: true, Known: true, Digest: proposalContentDigest(data), Mode: mode}
}

func conflictSnapshotsEqual(a, b conflictSnapshot) bool {
	if !a.Known || !b.Known || a.Exists != b.Exists {
		return false
	}
	if !a.Exists {
		return true
	}
	return a.Digest == b.Digest && a.Mode == b.Mode
}

// conflictWorkspaceRel maps an absolute workspace path to a display path. A
// path outside the workspace stays absolute rather than pretending to be
// relative.
func conflictWorkspaceRel(ws Workspace, abs string) string {
	root := ws.WorkDir()
	if root == "" {
		root = "."
	}
	rel, err := filepath.Rel(root, abs)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return normalizeTrackedPath(rel)
	}
	return normalizeTrackedPath(abs)
}

// shellMayMutate is deliberately conservative about what counts as evidence:
// a shell command that may write is recorded as an unknown mutation rather than
// letting it disappear from the conflict view.
func shellMayMutate(args string) bool {
	var params struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(args), &params) != nil {
		return false
	}
	cmd := strings.ToLower(params.Command)
	if cmd == "" {
		return false
	}
	for _, marker := range []string{
		">", ">>", "tee ", "rm ", "rm\t", "mv ", "cp ", "install ", "touch ",
		"mkdir ", "rmdir ", "sed -i", "perl -pi", "git apply", "patch ", "dd ",
		"truncate ", "chmod ", "chown ", "ln ", "tar -x", "unzip ", "python ",
		"python3 ", "node ", "ruby ", "out-file", "set-content", "add-content",
		"new-item", "remove-item", "move-item", "copy-item",
	} {
		if strings.Contains(cmd, marker) {
			return true
		}
	}
	return false
}

func shellDetail(args string) string {
	var params struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(args), &params) != nil {
		return "effects unknown"
	}
	cmd := strings.TrimSpace(params.Command)
	if len(cmd) > 120 {
		cmd = cmd[:117] + "..."
	}
	if cmd == "" {
		return "effects unknown"
	}
	return "may mutate: " + cmd
}
