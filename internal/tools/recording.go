package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
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
		// A non-zero exit still means the command ran and may have written
		// before failing. Record the unknown either way rather than turning a
		// partially-mutating failure into a false all-clear.
		if shellMayMutate(args) {
			detail := shellDetail(args)
			if err != nil {
				detail += " (command reported an error)"
			}
			rt.tracker.RecordUnknown(dispatch, toolName, detail)
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

	// Serialize evidence collection and the write for one path. Without this,
	// a concurrent writer's after-image can be attributed to this dispatch and
	// two divergent writes can look identical.
	unlock := lockConflictPath(abs)
	defer unlock()

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

type conflictPathLock struct {
	mu   sync.Mutex
	refs int
}

var (
	conflictPathLocksMu sync.Mutex
	conflictPathLocks   = make(map[string]*conflictPathLock)
)

// lockConflictPath serializes snapshot/write/snapshot for one path. The key is
// case-folded on case-insensitive hosts, matching the conflict comparison.
// Entries are refcounted and removed after the last holder releases, so a
// long-lived session does not retain one mutex per path it ever touched.
func lockConflictPath(abs string) func() {
	key := pathKey(normalizeTrackedPath(abs))
	conflictPathLocksMu.Lock()
	lock := conflictPathLocks[key]
	if lock == nil {
		lock = &conflictPathLock{}
		conflictPathLocks[key] = lock
	}
	lock.refs++
	conflictPathLocksMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		conflictPathLocksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(conflictPathLocks, key)
		}
		conflictPathLocksMu.Unlock()
	}
}

type conflictSnapshot struct {
	Exists bool
	Known  bool
	Digest string
	Mode   string
	Size   int64
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
		return conflictSnapshot{Exists: true, Mode: mode, Size: info.Size()}
	}
	data, err := ws.ReadFile(ctx, abs)
	if err != nil {
		return conflictSnapshot{Exists: true, Mode: mode, Size: info.Size()}
	}
	return conflictSnapshot{Exists: true, Known: true, Digest: proposalContentDigest(data), Mode: mode, Size: info.Size()}
}

func conflictSnapshotsEqual(a, b conflictSnapshot) bool {
	if a.Exists != b.Exists {
		return false
	}
	if !a.Exists {
		return true
	}
	if a.Known && b.Known {
		return a.Digest == b.Digest && a.Mode == b.Mode
	}
	if a.Known != b.Known {
		return false
	}
	// Both snapshots are content-unknown (large or special file). Compare the
	// observable shape so an idempotent rewrite is not reported as a change;
	// this is evidence of no observed shape change, not proof of identical
	// bytes.
	return a.Size == b.Size && a.Mode == b.Mode
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
	return shellCommandMayMutate(params.Command)
}

func shellCommandMayMutate(cmd string) bool {
	cmd = strings.ToLower(cmd)
	if strings.TrimSpace(cmd) == "" {
		return false
	}
	if shellHasRedirect(cmd) {
		return true
	}
	words := shellWords(cmd)
	mutators := map[string]bool{
		"rm": true, "mv": true, "cp": true, "install": true, "touch": true,
		"mkdir": true, "rmdir": true, "tee": true, "dd": true, "truncate": true,
		"chmod": true, "chown": true, "ln": true, "patch": true, "unzip": true,
		"make": true, "cargo": true, "npm": true, "pnpm": true, "yarn": true,
		"pip": true, "pip3": true, "python": true, "python3": true, "node": true,
		"ruby": true,
	}
	for _, word := range words {
		if mutators[word] {
			return true
		}
	}
	for i, word := range words {
		next := ""
		if i+1 < len(words) {
			next = words[i+1]
		}
		switch {
		case (word == "sed" || word == "perl") && (next == "-i" || strings.HasPrefix(next, "-i")):
			return true
		case word == "git" && (next == "apply" || next == "checkout" || next == "commit" || next == "clean" || next == "reset" || next == "stash"):
			return true
		case word == "go" && (next == "generate" || next == "build" || next == "mod"):
			return true
		case word == "tar" && (next == "-x" || next == "--extract" || strings.HasPrefix(next, "-x")):
			return true
		}
	}
	// A nested interpreter is still a shell command. Inspect the string after
	// -c/-lc so `bash -lc "sed -i ..."` is not a false all-clear.
	for i, word := range words {
		if word != "bash" && word != "sh" && word != "zsh" && word != "powershell" && word != "pwsh" {
			continue
		}
		for j := i + 1; j < len(words); j++ {
			if !strings.HasPrefix(words[j], "-") || !strings.Contains(words[j], "c") {
				continue
			}
			if inner := strings.Join(words[j+1:], " "); inner != "" && shellCommandMayMutate(inner) {
				return true
			}
			break
		}
	}
	return false
}

// shellHasRedirect reports an output redirect outside quotes. Redirections to a
// file descriptor (2>&1, >&2) are not file writes.
func shellHasRedirect(cmd string) bool {
	runes := []rune(cmd)
	var quote rune
	escaped := false
	for i, r := range runes {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && quote != '\'':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == '>':
			if i+1 < len(runes) && runes[i+1] == '&' {
				continue
			}
			return true
		}
	}
	return false
}

// shellWords is a minimal shell lexer: it separates words outside quotes so a
// quoted > is not mistaken for a redirect and `make` is not hidden in a larger
// token.
func shellWords(cmd string) []string {
	var words []string
	var word strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	for _, r := range cmd {
		switch {
		case escaped:
			word.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t' || r == '\n' || r == '\r' ||
			r == ';' || r == '&' || r == '|':
			flush()
		default:
			word.WriteRune(r)
		}
	}
	flush()
	return words
}

func shellDetail(args string) string {
	var params struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(args), &params) != nil {
		return "effects unknown"
	}
	cmd := strings.TrimSpace(params.Command)
	if cmd == "" {
		return "effects unknown"
	}
	runes := []rune(cmd)
	if len(runes) > 120 {
		cmd = string(runes[:117]) + "..."
	}
	if !utf8.ValidString(cmd) {
		cmd = strings.ToValidUTF8(cmd, "?")
	}
	return "may mutate: " + cmd
}
