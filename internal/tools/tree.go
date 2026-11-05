package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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
	if req.DestExists && req.Force {
		if err := req.ws.RemoveAll(ctx, req.To); err != nil {
			return "", fmt.Errorf("copy_tree: replace destination: %w", err)
		}
	}
	stats, err := copyWorkspaceTree(ctx, req.ws, req.Path, req.To)
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
	if req.DestExists && req.Force {
		if err := req.ws.RemoveAll(ctx, req.To); err != nil {
			return "", fmt.Errorf("move_tree: replace destination: %w", err)
		}
	}
	stats, err := moveWorkspaceTree(ctx, req.ws, req.Path, req.To)
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
	dst, err := ws.ResolvePath(params.To)
	if err != nil {
		return treeRequest{}, fmt.Errorf("%s: %w", tool, err)
	}
	if src == dst {
		return treeRequest{}, fmt.Errorf("%s: source and destination are the same path", tool)
	}
	if pathWithin(ws, src, dst) {
		return treeRequest{}, fmt.Errorf("%s: destination %s is inside the source %s", tool, dst, src)
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

type treeStats struct {
	Files int
	Dirs  int
}

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

func moveWorkspaceTree(ctx context.Context, ws Workspace, src, dst string) (treeStats, error) {
	// A same-filesystem rename moves the whole tree without reading bytes.
	// If the substrate refuses (for example a local cross-device rename, or a
	// sandbox whose mv cannot cross mounts), fall back to copy + remove.
	if err := ws.Move(ctx, src, dst); err == nil {
		return countWorkspaceTree(ctx, ws, dst), nil
	} else {
		moveErr := err
		stats, cerr := copyWorkspaceTree(ctx, ws, src, dst)
		if cerr != nil {
			return stats, fmt.Errorf("rename failed (%v); cross-device copy also failed: %w", moveErr, cerr)
		}
		if rerr := ws.RemoveAll(ctx, src); rerr != nil {
			return stats, fmt.Errorf("copied %s to %s but could not remove the source: %w", src, dst, rerr)
		}
		return stats, nil
	}
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
