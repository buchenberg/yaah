package tools

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

// CopyTreeTool recursively duplicates a directory inside the workspace.
type CopyTreeTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*CopyTreeTool)(nil)
	_ WorkspaceSetter     = (*CopyTreeTool)(nil)
)

func (t *CopyTreeTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *CopyTreeTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *CopyTreeTool) Name() string        { return "copy_tree" }
func (t *CopyTreeTool) Description() string { return prompts.ToolDescription("copy_tree") }

func (t *CopyTreeTool) Schema() json.RawMessage { return treeTransferSchema() }
func (t *CopyTreeTool) IsDangerous(string) bool { return true }
func (t *CopyTreeTool) Execute(ctx context.Context, args string) (string, error) {
	req, err := prepareTreeTransfer(ctx, "copy_tree", workspaceOf(t.WS, t.PV), args)
	if err != nil {
		return "", err
	}
	var stats treeStats
	if req.DestExists && req.Force {
		stats, err = replaceTreeCopy(ctx, req.ws, req.Path, req.To)
	} else {
		stats, err = copyWorkspaceTree(ctx, req.ws, req.Path, req.To)
	}
	if err != nil {
		return "", fmt.Errorf("copy_tree: %w", err)
	}
	return fmt.Sprintf("Copied tree %s to %s (%d files, %d directories)", req.Path, req.To, stats.Files, stats.Dirs), nil
}

// MoveTreeTool relocates a directory inside the workspace.
type MoveTreeTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*MoveTreeTool)(nil)
	_ WorkspaceSetter     = (*MoveTreeTool)(nil)
)

func (t *MoveTreeTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *MoveTreeTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *MoveTreeTool) Name() string        { return "move_tree" }
func (t *MoveTreeTool) Description() string { return prompts.ToolDescription("move_tree") }

func (t *MoveTreeTool) Schema() json.RawMessage { return treeTransferSchema() }
func (t *MoveTreeTool) IsDangerous(string) bool { return true }
func (t *MoveTreeTool) Execute(ctx context.Context, args string) (string, error) {
	req, err := prepareTreeTransfer(ctx, "move_tree", workspaceOf(t.WS, t.PV), args)
	if err != nil {
		return "", err
	}
	var stats treeStats
	if req.DestExists && req.Force {
		stats, err = replaceTreeMove(ctx, req.ws, req.Path, req.To)
	} else {
		stats, err = moveWorkspaceTree(ctx, req.ws, req.Path, req.To)
	}
	if err != nil {
		return "", fmt.Errorf("move_tree: %w", err)
	}
	return fmt.Sprintf("Moved tree %s to %s (%d files, %d directories)", req.Path, req.To, stats.Files, stats.Dirs), nil
}

func treeTransferSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Absolute path of the directory to operate on."
			},
			"to": {
				"type": "string",
				"description": "Absolute destination path. It does not need to exist; pass force=true to replace an existing tree."
			},
			"force": {
				"type": "boolean",
				"description": "Replace an existing destination tree. Default false: an existing destination is refused.",
				"default": false
			},
			"mkdir_p": {
				"type": "boolean",
				"description": "Create missing parent directories of the destination. Default false: the parent must already exist.",
				"default": false
			}
		},
		"required": ["path", "to"]
	}`)
}

// treeRequest is the validated form of a copy_tree/move_tree call.
type treeRequest struct {
	ws         Workspace
	Path       string
	To         string
	Force      bool
	DestExists bool
}

func prepareTreeTransfer(ctx context.Context, tool string, ws Workspace, args string) (treeRequest, error) {
	var params struct {
		Path   string `json:"path"`
		To     string `json:"to"`
		Force  bool   `json:"force"`
		MkdirP bool   `json:"mkdir_p"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return treeRequest{}, fmt.Errorf("%s: invalid arguments: %w", tool, err)
	}
	if params.Path == "" {
		return treeRequest{}, fmt.Errorf("%s: path is required", tool)
	}
	if params.To == "" {
		return treeRequest{}, fmt.Errorf("%s: to is required", tool)
	}
	src, err := ws.ResolvePath(params.Path)
	if err != nil {
		return treeRequest{}, fmt.Errorf("%s: %w", tool, err)
	}
	dst, err := ws.ResolveWritePath(params.To)
	if err != nil {
		return treeRequest{}, fmt.Errorf("%s: %w", tool, err)
	}
	if src == dst {
		return treeRequest{}, fmt.Errorf("%s: source and destination are the same path", tool)
	}
	if pathWithin(ws, src, dst) {
		return treeRequest{}, fmt.Errorf("%s: destination %s is inside the source %s", tool, dst, src)
	}
	if pathWithin(ws, dst, src) {
		// force=true would otherwise remove the destination before the
		// copy, and the destination is an ancestor of the source: the
		// source would be deleted with it.
		return treeRequest{}, fmt.Errorf("%s: destination %s is an ancestor of the source %s; tree operations require non-overlapping paths", tool, dst, src)
	}

	srcInfo, err := ws.Lstat(ctx, src)
	if err != nil {
		return treeRequest{}, fmt.Errorf("%s: %s: %w", tool, src, err)
	}
	if srcInfo.Mode()&fs.ModeSymlink != 0 {
		return treeRequest{}, fmt.Errorf("%s: %s is a symlink; tree operations do not follow or copy links", tool, src)
	}
	if !srcInfo.IsDir() {
		return treeRequest{}, fmt.Errorf("%s: %s is not a directory; use copy or move for a regular file", tool, src)
	}

	req := treeRequest{ws: ws, Path: src, To: dst, Force: params.Force}
	dstInfo, err := ws.Lstat(ctx, dst)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return treeRequest{}, fmt.Errorf("%s: %s: %w", tool, dst, err)
	case dstInfo.Mode()&fs.ModeSymlink != 0:
		return treeRequest{}, fmt.Errorf("%s: destination %s is a symlink; pass the real path or remove the link", tool, dst)
	case !params.Force:
		return treeRequest{}, fmt.Errorf("%s: destination %s already exists; pass force=true to replace it", tool, dst)
	default:
		req.DestExists = true
	}

	parent := wsPathDir(ws, dst)
	if err := refuseSymlinkAncestors(ctx, ws, parent); err != nil {
		return treeRequest{}, fmt.Errorf("%s: %w", tool, err)
	}
	if params.MkdirP {
		if err := ws.MkdirAll(ctx, parent, 0o755); err != nil {
			return treeRequest{}, fmt.Errorf("%s: create destination parent %s: %w", tool, parent, err)
		}
	}
	if info, statErr := ws.Stat(ctx, parent); statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return treeRequest{}, fmt.Errorf("%s: destination parent %s does not exist; create it first with mkdir or pass mkdir_p=true", tool, parent)
		}
		return treeRequest{}, fmt.Errorf("%s: %s: %w", tool, parent, statErr)
	} else if !info.IsDir() {
		return treeRequest{}, fmt.Errorf("%s: destination parent %s is not a directory", tool, parent)
	}
	return req, nil
}

// pathWithin reports whether child is inside parent. It uses the workspace's
// path syntax so a POSIX sandbox path is not compared with Windows rules.
func pathWithin(ws Workspace, parent, child string) bool {
	if !ws.Local() {
		// Sandbox paths are POSIX and already cleaned by ResolvePath.
		prefix := strings.TrimSuffix(parent, "/") + "/"
		return strings.HasPrefix(child+"/", prefix)
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!strings.HasPrefix(rel, "../")
}

func workspaceRel(ws Workspace, base, target string) (string, error) {
	if ws.Local() {
		return filepath.Rel(base, target)
	}
	// Sandbox paths are POSIX and have no path.Rel analogue; every caller
	// only needs a descendant relationship.
	base = path.Clean(base)
	target = path.Clean(target)
	if base == target {
		return ".", nil
	}
	prefix := strings.TrimSuffix(base, "/") + "/"
	if !strings.HasPrefix(target, prefix) {
		return "", fmt.Errorf("%s is not inside %s", target, base)
	}
	return strings.TrimPrefix(target, prefix), nil
}

// refuseSymlinkAncestors checks every component from the workspace root to
// target, including target. The sandbox path layer is lexical, so a symlink
// above a destination is otherwise invisible to containment.
func refuseSymlinkAncestors(ctx context.Context, ws Workspace, target string) error {
	root := ws.WorkDir()
	if root == "" {
		return nil
	}
	rel, err := workspaceRel(ws, root, target)
	if err != nil || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) ||
		strings.HasPrefix(rel, "../") {
		return nil
	}
	return refuseSymlinkComponents(ctx, ws, root, rel)
}

// refuseSymlinkComponents rejects an existing symlink in base or in any
// component of rel below it. It stops at the first missing component: the
// rest is created by MkdirAll and cannot already redirect through a link.
func refuseSymlinkComponents(ctx context.Context, ws Workspace, base, rel string) error {
	if info, err := ws.Lstat(ctx, base); err == nil {
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; refusing to write through it", base)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: %w", base, err)
	}
	cur := base
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." {
			continue
		}
		cur = ws.Join(cur, part)
		info, err := ws.Lstat(ctx, cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", cur, err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; refusing to write through it", cur)
		}
		if !info.IsDir() {
			return nil
		}
	}
	return nil
}

type treeStats struct {
	Files int
	Dirs  int
}

// treeSourceRemovalError distinguishes a complete cross-device copy whose
// source cleanup failed from a partial copy. The destination holds every
// source byte, so callers must keep it rather than roll it back.
type treeSourceRemovalError struct {
	Src   string
	Dst   string
	Stats treeStats
	Err   error
}

func (e *treeSourceRemovalError) Error() string {
	return fmt.Sprintf("copied %s to %s but could not remove the source: %v", e.Src, e.Dst, e.Err)
}

func (e *treeSourceRemovalError) Unwrap() error { return e.Err }

func copyWorkspaceTree(ctx context.Context, ws Workspace, src, dst string) (treeStats, error) {
	info, err := ws.Lstat(ctx, src)
	if err != nil {
		return treeStats{}, err
	}
	return copyTreeDir(ctx, ws, src, dst, info.Mode().Perm())
}

func copyTreeDir(ctx context.Context, ws Workspace, src, dst string, perm fs.FileMode) (treeStats, error) {
	stats := treeStats{Dirs: 1}
	if err := ws.MkdirAll(ctx, dst, perm); err != nil {
		return stats, fmt.Errorf("create %s: %w", dst, err)
	}
	entries, err := ws.ReadDir(ctx, src)
	if err != nil {
		return stats, fmt.Errorf("read %s: %w", src, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		sp := ws.Join(src, entry.Name())
		dp := ws.Join(dst, entry.Name())
		info, err := ws.Lstat(ctx, sp)
		if err != nil {
			return stats, fmt.Errorf("%s: %w", sp, err)
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			return stats, fmt.Errorf("%s is a symlink; tree operations do not follow or copy links", sp)
		case info.IsDir():
			sub, serr := copyTreeDir(ctx, ws, sp, dp, info.Mode().Perm())
			stats.add(sub)
			if serr != nil {
				return stats, serr
			}
		case info.Mode().IsRegular():
			if err := ws.Copy(ctx, sp, dp); err != nil {
				return stats, fmt.Errorf("copy %s: %w", sp, err)
			}
			stats.Files++
		default:
			return stats, fmt.Errorf("%s is not a regular file or directory", sp)
		}
	}
	return stats, nil
}

// replaceTreeCopy stages a complete copy beside the destination and only
// removes the old destination once the copy exists. A failure while reading
// the source leaves the old destination untouched. The staging directory is
// named in any promote error so a failed swap does not hide the copied data.
func replaceTreeCopy(ctx context.Context, ws Workspace, src, dst string) (treeStats, error) {
	staging, err := treeStagingPath(ctx, ws, dst)
	if err != nil {
		return treeStats{}, err
	}
	stats, err := copyWorkspaceTree(ctx, ws, src, staging)
	if err != nil {
		_ = ws.RemoveAll(ctx, staging)
		return stats, err
	}
	backup, err := treeStagingPath(ctx, ws, dst)
	if err != nil {
		_ = ws.RemoveAll(ctx, staging)
		return stats, err
	}
	if err := ws.Move(ctx, dst, backup); err != nil {
		_ = ws.RemoveAll(ctx, staging)
		return stats, fmt.Errorf("stage existing destination %s aside: %w", dst, err)
	}
	if err := ws.Move(ctx, staging, dst); err != nil {
		if restoreErr := ws.Move(ctx, backup, dst); restoreErr != nil {
			return stats, fmt.Errorf("promote staged copy: %v; staged copy at %s, old destination backup at %s; restore failed: %w", err, staging, backup, restoreErr)
		}
		_ = ws.RemoveAll(ctx, staging)
		return stats, fmt.Errorf("promote staged copy: %w (old destination restored)", err)
	}
	if err := ws.RemoveAll(ctx, backup); err != nil {
		return stats, fmt.Errorf("copied to %s but could not remove the backup %s: %w", dst, backup, err)
	}
	return stats, nil
}

func replaceTreeMove(ctx context.Context, ws Workspace, src, dst string) (treeStats, error) {
	backup, err := treeStagingPath(ctx, ws, dst)
	if err != nil {
		return treeStats{}, err
	}
	if err := ws.Move(ctx, dst, backup); err != nil {
		return treeStats{}, fmt.Errorf("stage existing destination %s aside: %w", dst, err)
	}
	stats, err := moveWorkspaceTree(ctx, ws, src, dst)
	if err != nil {
		var removal *treeSourceRemovalError
		var partial *partialMoveError
		if errors.As(err, &removal) || errors.As(err, &partial) {
			// The copy is complete but source cleanup failed. dst may hold the
			// only surviving copy of files already removed from src, so keep
			// it and the old destination backup rather than rolling either back.
			stats = countWorkspaceTree(ctx, ws, dst)
			return stats, fmt.Errorf("move created %s but could not remove source %s (%v); new destination kept, old destination backup at %s; inspect before retrying", dst, src, err, backup)
		}
		// A copy that failed before completing leaves src intact, so the
		// partial destination is safe to clear before restoring the old tree.
		_ = ws.RemoveAll(ctx, dst)
		if restoreErr := ws.Move(ctx, backup, dst); restoreErr != nil {
			return stats, fmt.Errorf("move failed (%v); old destination backup at %s; restore failed: %w", err, backup, restoreErr)
		}
		return stats, err
	}
	if err := ws.RemoveAll(ctx, backup); err != nil {
		return stats, fmt.Errorf("moved to %s but could not remove the backup %s: %w", dst, backup, err)
	}
	return stats, nil
}

func treeStagingPath(ctx context.Context, ws Workspace, dst string) (string, error) {
	parent := wsPathDir(ws, dst)
	for attempt := 0; attempt < 4; attempt++ {
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return "", fmt.Errorf("staging name: %w", err)
		}
		// A fixed short prefix keeps the name under NAME_MAX even when the
		// destination basename is already near the limit.
		candidate := ws.Join(parent, fmt.Sprintf(".yaah-tmp-%x", suffix))
		_, err := ws.Lstat(ctx, candidate)
		if errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("could not allocate a staging path beside %s", dst)
}

func moveWorkspaceTree(ctx context.Context, ws Workspace, src, dst string) (treeStats, error) {
	// A same-filesystem rename moves the whole tree without reading bytes.
	// Only a genuine cross-device refusal falls back to copy + remove; any
	// other rename error must not merge into a destination the pre-check
	// refused.
	err := ws.Move(ctx, src, dst)
	if err == nil {
		return countWorkspaceTree(ctx, ws, dst), nil
	}
	if !isCrossDeviceErr(err) {
		return treeStats{}, err
	}
	stats, cerr := copyWorkspaceTree(ctx, ws, src, dst)
	if cerr != nil {
		return stats, fmt.Errorf("rename failed (%v); cross-device copy also failed: %w", err, cerr)
	}
	if rerr := ws.RemoveAll(ctx, src); rerr != nil {
		return stats, &treeSourceRemovalError{Src: src, Dst: dst, Stats: stats, Err: rerr}
	}
	return stats, nil
}

// countWorkspaceTree reports the files/directories in a moved tree for the
// tool result. A read failure stops the count; the move itself already
// succeeded, so a partial count is preferable to reporting an error.
func countWorkspaceTree(ctx context.Context, ws Workspace, root string) treeStats {
	stats := treeStats{Dirs: 1}
	entries, err := ws.ReadDir(ctx, root)
	if err != nil {
		return stats
	}
	for _, entry := range entries {
		p := ws.Join(root, entry.Name())
		info, err := ws.Lstat(ctx, p)
		if err != nil {
			continue
		}
		switch {
		case info.IsDir():
			stats.add(countWorkspaceTree(ctx, ws, p))
		case info.Mode().IsRegular():
			stats.Files++
		}
	}
	return stats
}

func (s *treeStats) add(other treeStats) {
	s.Files += other.Files
	s.Dirs += other.Dirs
}
