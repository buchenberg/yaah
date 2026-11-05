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
				"description": "Absolute destination path for the file. Its parent directory must already exist; create it with mkdir first."
			},
			"force": {
				"type": "boolean",
				"description": "Replace the destination when it already exists. Default false: an existing destination is refused.",
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
//   - the source must exist and be a regular file as the path layer leaves it —
//     a directory, FIFO, socket, or device is refused with a clear error, which
//     keeps the tool's semantics bounded and the host copy from blocking on a
//     special file. A symlink follows the substrate: the host path validator
//     resolves it first (like every other file tool, transferring the target),
//     while a lexical-path substrate such as the container sandbox refuses it;
//   - the destination must not be a directory; a symlink destination is refused
//     on a lexical-path substrate, while the host resolves a valid link first
//     and applies the clobber rule to its target;
//   - an existing destination is refused unless force is set.
//
// The no-clobber rule is check-then-act: a writer that creates dst between the
// Lstat here and the substrate primitive below can still be overwritten.
// In-process dispatches are serialized by the ConflictTracker locks; an
// external writer racing a dispatch is a documented limitation — no portable
// no-replace rename exists on every supported OS.
//
// Both paths are resolved through the workspace's PathValidator first, so deny
// patterns and workspace-root containment apply exactly as they do for
// read/write/edit/delete.
func prepareTransfer(ctx context.Context, tool string, ws Workspace, args string) (transferRequest, error) {
	var params struct {
		Path   string `json:"path"`
		To     string `json:"to"`
		Force  bool   `json:"force"`
		MkdirP bool   `json:"mkdir_p"`
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
	// reads. The source must be a regular file as the path layer leaves it, so
	// directories are out of scope, a FIFO, socket, or device would block or
	// stream forever, and a symlink would diverge between the substrates (mv
	// moves the link, cp dereferences it). All are refused with a clear error.
	//
	// On the host, ResolvePath has already followed a final symlink, so this
	// sees the link's target and a valid link is transferred like any other
	// file tool. On the lexical sandbox path it sees the link itself, and the
	// IsRegular check refuses it — the documented substrate difference.
	info, err := ws.Lstat(ctx, src)
	if err != nil {
		return transferRequest{}, fmt.Errorf("%s: %s: %w", tool, src, err)
	}
	if !info.Mode().IsRegular() {
		return transferRequest{}, fmt.Errorf("%s: %s is not a regular file; %s operates on regular files only", tool, src, tool)
	}

	dstInfo, err := ws.Lstat(ctx, dst)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// The common case: a fresh destination.
	case err != nil:
		return transferRequest{}, fmt.Errorf("%s: %s: %w", tool, dst, err)
	case dstInfo.Mode()&fs.ModeSymlink != 0:
		// A symlink destination is refused on a lexical-path substrate: mv
		// would replace the link while cp writes through it — and for a link to
		// a directory the transfer would drop the file inside that directory.
		// Accepting one there would make the tools' semantics depend on which
		// primitive runs. On the host this branch is unreachable for a valid
		// link (ResolvePath already followed it, so the clobber rule applies to
		// its target) and a dangling link fails in the path layer; it remains
		// here so a substrate that stops resolving cannot silently accept a
		// link.
		return transferRequest{}, fmt.Errorf("%s: destination %s is a symlink; pass the real path or remove the link", tool, dst)
	case dstInfo.IsDir():
		return transferRequest{}, fmt.Errorf("%s: destination %s is a directory", tool, dst)
	case !req.Force:
		return transferRequest{}, fmt.Errorf("%s: destination %s already exists; pass force=true to replace it", tool, dst)
	}

	// The destination parent must already exist. Creating it here would make
	// copy/move implicit directory creators; the explicit mkdir tool is the
	// one place directories are made, and this check turns the substrate's
	// opaque "create temp: no such file or directory" into an actionable
	// message.
	parent := wsPathDir(ws, dst)
	if params.MkdirP {
		if err := ws.MkdirAll(ctx, parent, 0o755); err != nil {
			return transferRequest{}, fmt.Errorf("%s: create destination parent %s: %w", tool, parent, err)
		}
	}
	switch info, statErr := ws.Stat(ctx, parent); {
	case errors.Is(statErr, fs.ErrNotExist):
		return transferRequest{}, fmt.Errorf("%s: destination parent %s does not exist; create it first with mkdir", tool, parent)
	case statErr != nil:
		return transferRequest{}, fmt.Errorf("%s: %s: %w", tool, parent, statErr)
	case !info.IsDir():
		return transferRequest{}, fmt.Errorf("%s: destination parent %s is not a directory", tool, parent)
	}
	return req, nil
}
