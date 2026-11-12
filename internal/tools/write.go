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

// WriteTool writes content to a file, overwriting if it exists.
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
			"filePath": {"type": "string", "description": "The absolute path to the file to write"}
		},
		"required": ["content", "filePath"]
	}`)
}

func (t *WriteTool) IsDangerous(argsJSON string) bool { return true }

func (t *WriteTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Content  string `json:"content"`
		FilePath string `json:"filePath"`
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
	receipt, err := withFileWrite(ctx, ws, params.FilePath, func(cur []byte, _ fs.FileMode) ([]byte, error) {
		SetSupervisionMeta(ctx, types.SupervisionKeyFileExisted, cur != nil)
		return []byte(params.Content), nil
	})
	if err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	return fmt.Sprintf("Wrote %d bytes to %s\n%s", len(params.Content), params.FilePath, receipt.String()), nil
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
