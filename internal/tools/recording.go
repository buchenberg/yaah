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

// selfGuardingTool is implemented by single-path tools that take the
// per-path write guard (withFileWrite / lockConflictPath, fileguard.go)
// inside their own Execute. RecordingTool must not hold the per-path lock
// across such a tool: the lock is refcounted but not reentrant per
// goroutine, so the wrapper's acquisition would block the wrapped tool's
// own forever.
type selfGuardingTool interface{ selfGuardsPath() }

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

	if toolName == "move" || toolName == "copy" {
		return rt.recordTransfer(ctx, dispatch, toolName, args)
	}

	if toolName == "copy_tree" || toolName == "move_tree" || toolName == "bulk_files" {
		result, err := rt.inner.Execute(ctx, args)
		var params struct {
			Path    string `json:"path"`
			To      string `json:"to"`
			Action  string `json:"action"`
			Pattern string `json:"pattern"`
			DryRun  bool   `json:"dry_run"`
		}
		if json.Unmarshal([]byte(args), &params) == nil {
			if toolName == "bulk_files" && params.DryRun {
				return result, err
			}
			detail := toolName
			if toolName == "bulk_files" {
				detail += " " + params.Action + " " + params.Pattern
			} else {
				detail += " " + params.Path + " -> " + params.To
			}
			if err != nil {
				detail += " (command reported an error)"
			}
			// A tree or bulk operation can change an arbitrary set the
			// recorder cannot enumerate cheaply; record an unknown rather
			// than let a parallel worker see a false all-clear.
			rt.tracker.RecordUnknown(dispatch, toolName, boundedRunes(detail, 120))
		}
		return result, err
	}

	if toolName == "rmdir" {
		result, err := rt.inner.Execute(ctx, args)
		// An empty-only rmdir removes no files, so it needs no evidence. A
		// recursive rmdir can remove an arbitrary set the recorder cannot
		// enumerate cheaply, so record an unknown rather than a false
		// all-clear: a parallel worker editing a file inside the tree must not
		// see "nothing changed".
		var params struct {
			Path      string `json:"path"`
			Recursive bool   `json:"recursive"`
		}
		if json.Unmarshal([]byte(args), &params) == nil && params.Recursive && params.Path != "" {
			// Reserve the prefix and failure marker before bounding the path:
			// truncating the assembled string would drop the marker for a long
			// path, making a failed recursive delete look like a clean one.
			detail := "rmdir -r "
			suffix := ""
			if err != nil {
				suffix = " (command reported an error)"
			}
			budget := 120 - utf8.RuneCountInString(detail) - utf8.RuneCountInString(suffix)
			rt.tracker.RecordUnknown(dispatch, toolName, detail+boundedRunes(params.Path, budget)+suffix)
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
	// two divergent writes can look identical. Guard-routed tools
	// (selfGuardingTool) take the same lock inside their own Execute, so
	// holding it here would re-enter the non-reentrant lock on this same
	// goroutine and deadlock; their own critical section provides the
	// serialization this sequence wants.
	if _, guarded := rt.inner.(selfGuardingTool); !guarded {
		unlock := lockConflictPath(abs)
		defer unlock()
	}

	before := captureConflictSnapshot(ctx, ws, abs)
	result, err := rt.inner.Execute(ctx, args)
	if err != nil {
		return result, err
	}
	rt.recordSnapshot(ctx, dispatch, toolName, ws, abs, before)
	return result, nil
}

// recordSnapshot captures abs's after-image and records the change when it
// differs from before. Single-path tools (write/edit/delete) and the two-path
// ones (move/copy) both record through here, so the evidence shape cannot
// diverge between them.
func (rt *RecordingTool) recordSnapshot(ctx context.Context, dispatch ConflictDispatch, toolName string, ws Workspace, abs string, before conflictSnapshot) {
	after := captureConflictSnapshot(ctx, ws, abs)
	if conflictSnapshotsEqual(before, after) {
		return
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
}

// recordTransfer records both ends of a move/copy. One call changes two paths —
// the source disappears, the destination appears or changes — so a
// single-path record would report half of what happened and let a parallel
// worker's write to the source go unseen.
func (rt *RecordingTool) recordTransfer(ctx context.Context, dispatch ConflictDispatch, toolName, args string) (string, error) {
	src, dst := transferPaths(args)
	ws := rt.workspace()
	if src == "" || dst == "" || ws == nil {
		return rt.inner.Execute(ctx, args)
	}
	absSrc, err := ws.ResolvePath(src)
	if err != nil {
		return rt.inner.Execute(ctx, args)
	}
	absDst, err := ws.ResolvePath(dst)
	if err != nil {
		return rt.inner.Execute(ctx, args)
	}

	// Both paths in one critical section. The locks are taken in sorted order so
	// two dispatches moving A→B and B→A cannot deadlock.
	unlock := lockConflictPathSet(absSrc, absDst)
	defer unlock()

	beforeSrc := captureConflictSnapshot(ctx, ws, absSrc)
	beforeDst := captureConflictSnapshot(ctx, ws, absDst)
	result, err := rt.inner.Execute(ctx, args)
	if err != nil {
		// A cross-filesystem move can create the destination and then fail to
		// remove the source. The destination change is real, so record it even
		// though the call reports an error; otherwise the next writer sees no
		// evidence, and a retry without force is refused as a clobber.
		var partial *partialMoveError
		if toolName == "move" && errors.As(err, &partial) {
			rt.recordSnapshot(ctx, dispatch, toolName, ws, absDst, beforeDst)
		}
		return result, err
	}
	// A copy leaves the source untouched: recording it would report a change
	// that never happened — and for an unknown snapshot (a special file, or a
	// file over the hash cap) invent a conflict between two readers of the same
	// file. Only a move changes the source end.
	if toolName == "move" {
		rt.recordSnapshot(ctx, dispatch, toolName, ws, absSrc, beforeSrc)
	}
	rt.recordSnapshot(ctx, dispatch, toolName, ws, absDst, beforeDst)
	return result, nil
}

// transferPaths reads the path/to pair the move and copy tools share.
func transferPaths(args string) (src, dst string) {
	var params struct {
		Path string `json:"path"`
		To   string `json:"to"`
	}
	if json.Unmarshal([]byte(args), &params) != nil {
		return "", ""
	}
	return params.Path, params.To
}

// The per-path lock this file's snapshot/write/snapshot sequence depends on
// (lockConflictPath, lockConflictPathSet) lives in fileguard.go, beside the
// write guard that shares it.

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
	// Unknown evidence is never a no-op: a large/special/unreadable path must
	// surface as an unverified change rather than disappearing. The tracker
	// renders that separately from a content-confirmed conflict.
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
	// On Windows the root and the resolved path can spell the same directory
	// differently: the validator canonicalizes through EvalSymlinks, which
	// yields the 8.3 short form (C:\Users\RUNNER~1\…) while a root captured
	// before that keeps the long form, so Rel sees two unrelated trees. Retry
	// with both sides canonicalized before falling back to the absolute path —
	// a receipt or a refusal naming C:/Users/RUNNER~1/AppData/…/x.go instead of
	// x.go is noise the reader has to decode.
	// Canonicalize the root first: on Windows the resolved path is already in
	// the short form, so relating it to a canonical root needs no second
	// lookup, and relating works even for a path that does not exist yet.
	if realRoot, err := filepath.EvalSymlinks(root); err == nil && realRoot != "" {
		if rel, err := filepath.Rel(realRoot, abs); err == nil &&
			rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return normalizeTrackedPath(rel)
		}
		// The path may itself have been reached through a symlinked
		// directory while the root is real.
		if realAbs, err := filepath.EvalSymlinks(abs); err == nil && realAbs != "" {
			if rel, err := filepath.Rel(realRoot, realAbs); err == nil &&
				rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return normalizeTrackedPath(rel)
			}
		}
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
	return "may mutate: " + boundedDetail(cmd)
}

// boundedDetail keeps an evidence detail safe for the parent-facing conflict
// report: at most 120 runes, with invalid UTF-8 (legal in a POSIX path)
// replaced rather than stored raw. Every caller-supplied fragment that reaches
// FileRecord.Detail goes through here.
func boundedDetail(s string) string { return boundedRunes(s, 120) }

// boundedRunes sanitizes s and limits it to max runes, keeping a trailing
// ellipsis when it truncates. A caller that appends a fixed marker must bound
// only the variable part and reserve the marker's runes, so the marker cannot
// be truncated away.
func boundedRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "?")
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}
