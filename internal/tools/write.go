package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/types"
)

// WriteTool writes content to a file. Creating a path needs nothing; replacing
// a file that already exists needs an explicit overwrite: true, because write
// means "replace this file wholesale" and a path that already holds content is
// usually a name collision rather than an intent (yaah-5hkb).
type WriteTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*WriteTool)(nil)
	_ WorkspaceSetter     = (*WriteTool)(nil)
)

func (t *WriteTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *WriteTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *WriteTool) Name() string        { return "write" }
func (t *WriteTool) Description() string { return prompts.ToolDescription("write") }

func (t *WriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"content": {"type": "string", "description": "The content to write to the file"},
			"filePath": {"type": "string", "description": "The absolute path to the file to write"},
			"overwrite": {"type": "boolean", "description": "Replace the file when it already exists. Required for an existing path: without it the write is refused and nothing changes"},
			"if_digest": {"type": "string", "description": "Optional sha256 of the content you expect to find. Accepts the form receipts print (sha256 plus a 12-character hex prefix) or a bare hex digest of 12 or more characters. The write is refused when the file differs, so an overwrite can be verified instead of blind"}
		},
		"required": ["content", "filePath"]
	}`)
}

func (t *WriteTool) IsDangerous(argsJSON string) bool { return true }

func (t *WriteTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Content   string `json:"content"`
		FilePath  string `json:"filePath"`
		Overwrite bool   `json:"overwrite"`
		IfDigest  string `json:"if_digest"`
	}
	if err := StrictToolArgs("write", t.Schema(), args, &params); err != nil {
		return "", err
	}
	if params.FilePath == "" {
		return "", fmt.Errorf("write: filePath is required")
	}
	ws := workspaceOf(t.WS, t.PV)
	resolved, err := ws.ResolveWritePath(params.FilePath)
	if err != nil {
		return "", writePathError(err)
	}
	params.FilePath = resolved

	// write replaces the file's whole content, so it runs inside the write
	// guard (fileguard.go): the snapshot, the notice, and the write share one
	// per-path critical section, and the guard's compare-and-swap refuses the
	// write when an out-of-band writer changed the file while mutate ran. A
	// concurrent in-process write to the same path can no longer interleave
	// with this one.
	//
	// The overwrite notice lives inside mutate: cur == nil is the guard's
	// "path did not exist", so the notice observes the same snapshot the
	// guard writes against — no second stat, no window between notice and
	// write. The notice is advisory (yaah-0fn): the destructive_file_guard
	// warns on an overwrite, it does not refuse one.
	//
	// The explicit-replace rule (yaah-5hkb) is enforced on that same
	// snapshot: mutate already holds the bytes that would be destroyed, so the
	// refusal, the if_digest check and the stash all run against the content
	// the write is about to replace — never against a stale second read.
	var stashPath string
	var stashed int
	receipt, err := withFileWrite(ctx, ws, params.FilePath, func(cur []byte, _ fs.FileMode) ([]byte, error) {
		SetSupervisionMeta(ctx, types.SupervisionKeyFileExisted, cur != nil)
		if cur == nil {
			if params.IfDigest != "" {
				return nil, fmt.Errorf("write: if_digest was given but %s does not exist; drop if_digest to create it", params.FilePath)
			}
			return []byte(params.Content), nil
		}
		if params.IfDigest != "" && !digestMatches(params.IfDigest, fileContentDigest(cur)) {
			return nil, &DigestMismatchError{
				Path:     params.FilePath,
				Expected: params.IfDigest,
				Found:    fileContentDigest(cur),
			}
		}
		if !params.Overwrite {
			return nil, &OverwriteRefusedError{
				Path:  params.FilePath,
				Bytes: len(cur),
				Lines: lineCount(cur),
			}
		}
		stashed = len(cur)
		path, stashErr := stashOverwrite(ctx, ws, params.FilePath, cur)
		if stashErr != nil {
			return nil, fmt.Errorf("write: %w", stashErr)
		}
		stashPath = path
		return []byte(params.Content), nil
	})
	if err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	// The receipt stays the LAST line: it is the line consumers and tests read,
	// so the stash note goes above it rather than after.
	msg := fmt.Sprintf("Wrote %d bytes to %s", len(params.Content), params.FilePath)
	if stashPath != "" {
		msg += fmt.Sprintf("\nprevious %d bytes stashed at %s", stashed, stashPath)
	}
	msg += "\n" + receipt.String()
	return msg, nil
}

// selfGuardsPath marks write as taking the per-path write guard inside its
// own Execute. The recording wrapper (recording.go) must not hold the same
// per-path lock across it: the lock is not reentrant per goroutine.
func (*WriteTool) selfGuardsPath() {}

// writePathError adds the write-specific remedy to a containment refusal.
// The PathRefusalError already names both the target and the workspace root;
// this tells the model where disposable files belong so it does not retry the
// same out-of-workspace path.
func writePathError(err error) error {
	var refusal *PathRefusalError
	if errors.As(err, &refusal) && refusal.Root != "" {
		return fmt.Errorf("write: %w; use .scratch/ for disposable files inside the workspace", err)
	}
	return err
}
