package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/buchenberg/yaah/internal/prompts"
)

// MoveTool renames a file inside the workspace.
//
// It exists because a sub-agent often has no shell: the bash class is blocked
// for sub-dispatches by policy, and the git tool's whitelist deliberately
// excludes mv. Without this tool a sandboxed worker could create a file but
// never rename it.
//
// The bytes are moved by the substrate's own rename, so nothing is decoded,
// re-encoded, or passed through the model: a binary file survives intact.
type MoveTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*MoveTool)(nil)
	_ WorkspaceSetter     = (*MoveTool)(nil)
)

func (t *MoveTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *MoveTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *MoveTool) Name() string        { return "move" }
func (t *MoveTool) Description() string { return prompts.ToolDescription("move") }

func (t *MoveTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Absolute path of the file to move."
			},
			"to": {
				"type": "string",
				"description": "Absolute destination path for the file. Its parent directory must already exist."
			},
			"force": {
				"type": "boolean",
				"description": "Replace the destination when it already exists. Default false: an existing destination is refused.",
				"default": false
			}
		},
		"required": ["path", "to"]
	}`)
}

// IsDangerous is true for every call: a move relocates a file the user has.
func (t *MoveTool) IsDangerous(string) bool { return true }

func (t *MoveTool) Execute(ctx context.Context, args string) (string, error) {
	req, err := prepareTransfer(ctx, "move", workspaceOf(t.WS, t.PV), args)
	if err != nil {
		return "", err
	}
	if err := req.ws.Move(ctx, req.Path, req.To); err != nil {
		return "", fmt.Errorf("move: %w", err)
	}
	return fmt.Sprintf("Moved %s to %s", req.Path, req.To), nil
}

// transferRequest is the shared, validated form of a move/copy call. The two
// tools differ only in which workspace primitive they invoke, so argument
// decoding and every safety rule live here rather than being copied — a rule
// added to one file cannot be missing from the other.
type transferRequest struct {
	ws    Workspace
	Path  string // resolved source
	To    string // resolved destination
	Force bool
}

// prepareTransfer decodes and validates a move/copy argument object.
//
// Three rules are enforced for both tools, by construction rather than by the
// substrate's replace semantics (host rename, sandbox mv/cp, and the in-memory
// double all disagree on clobbering, so the rule is applied once, above them):
//
//   - the source must exist and be a file — moving or copying a directory is
//     refused with a clear error, which keeps the tool's semantics bounded;
//   - the destination must not be a directory;
//   - an existing destination is refused unless force is set.
//
// Both paths are resolved through the workspace's PathValidator first, so deny
// patterns and workspace-root containment apply exactly as they do for
// read/write/edit/delete.
func prepareTransfer(ctx context.Context, tool string, ws Workspace, args string) (transferRequest, error) {
	var params struct {
		Path  string `json:"path"`
		To    string `json:"to"`
		Force bool   `json:"force"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return transferRequest{}, fmt.Errorf("%s: invalid arguments: %w", tool, err)
	}
	if params.Path == "" {
		return transferRequest{}, fmt.Errorf("%s: path is required", tool)
	}
	if params.To == "" {
		return transferRequest{}, fmt.Errorf("%s: to is required", tool)
	}

	req := transferRequest{ws: ws, Force: params.Force}
	src, err := ws.ResolvePath(params.Path)
	if err != nil {
		return transferRequest{}, fmt.Errorf("%s: %w", tool, err)
	}
	dst, err := ws.ResolvePath(params.To)
	if err != nil {
		return transferRequest{}, fmt.Errorf("%s: %w", tool, err)
	}
	req.Path, req.To = src, dst

	// Lstat, not Stat: the entry at the path is what a rename moves and a copy
	// reads, so a symlink's own type is the honest answer to "is this a
	// directory". A symlink to a directory is moved as the link it is.
	info, err := ws.Lstat(ctx, src)
	if err != nil {
		return transferRequest{}, fmt.Errorf("%s: %s: %w", tool, src, err)
	}
	if info.IsDir() {
		return transferRequest{}, fmt.Errorf("%s: %s is a directory; %s operates on files only", tool, src, tool)
	}

	dstInfo, err := ws.Lstat(ctx, dst)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// The common case: a fresh destination.
	case err != nil:
		return transferRequest{}, fmt.Errorf("%s: %s: %w", tool, dst, err)
	case dstInfo.IsDir():
		return transferRequest{}, fmt.Errorf("%s: destination %s is a directory", tool, dst)
	case !req.Force:
		return transferRequest{}, fmt.Errorf("%s: destination %s already exists; pass force=true to replace it", tool, dst)
	}
	return req, nil
}
