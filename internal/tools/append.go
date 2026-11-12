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

// AppendTool appends to a file without reading or rewriting it. It is the
// log/ledger counterpart to write: concurrent sub-agents and background
// processes can add records without a read-modify-write race.
type AppendTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*AppendTool)(nil)
	_ WorkspaceSetter     = (*AppendTool)(nil)
)

func (t *AppendTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *AppendTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *AppendTool) Name() string        { return "append" }
func (t *AppendTool) Description() string { return prompts.ToolDescription("append") }

func (t *AppendTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "The absolute path of the file to append to; it is created when missing."},
			"content": {"type": "string", "description": "The text to append."}
		},
		"required": ["filePath", "content"]
	}`)
}

func (t *AppendTool) IsDangerous(string) bool { return true }

func (t *AppendTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		FilePath string `json:"filePath"`
		Content  string `json:"content"`
	}
	if err := StrictToolArgs("append", t.Schema(), args, &params); err != nil {
		return "", err
	}
	if params.FilePath == "" {
		return "", fmt.Errorf("append: filePath is required")
	}
	ws := workspaceOf(t.WS, t.PV)
	resolved, err := ws.ResolveWritePath(params.FilePath)
	if err != nil {
		return "", fmt.Errorf("append: %w", err)
	}
	params.FilePath = resolved

	// append is the log/ledger primitive, so it must not read-modify-write
	// and must not go through withFileWrite. It holds the per-path lock for
	// the whole call instead: the substrate's append primitive (O_APPEND on
	// the local lane) already keeps two concurrent appends from losing each
	// other — there is no compare-and-swap here for exactly that reason —
	// and the lock is what serializes an append against a concurrent
	// in-process rewrite (write/edit under withFileWrite): the rewrite
	// cannot swap the file out from under an append in flight, and an
	// append cannot land inside a writer's compare-and-swap window and
	// make that write look stale.
	unlock := lockConflictPath(params.FilePath)
	defer unlock()

	if _, err := ws.Stat(ctx, params.FilePath); err == nil {
		SetSupervisionMeta(ctx, types.SupervisionKeyFileExisted, true)
	} else if errors.Is(err, fs.ErrNotExist) {
		SetSupervisionMeta(ctx, types.SupervisionKeyFileExisted, false)
	}

	if err := ws.Append(ctx, params.FilePath, []byte(params.Content), 0o644); err != nil {
		return "", fmt.Errorf("append: %w", err)
	}
	// Receipt line, with no digest on purpose. Appends cannot lose each
	// other — the substrate's append primitive is append-atomic and the
	// per-path lock above orders this call against in-process rewrites — so
	// a post-write digest would cost a full read of the file for no
	// diagnostic gain. A before -> after size pair would likewise need a
	// stat after the append; the appended byte count is exact by
	// construction and is all the receipt needs here.
	return fmt.Sprintf("Appended %d bytes to %s\nappended to %s (+%d bytes)",
		len(params.Content), params.FilePath, params.FilePath, len(params.Content)), nil
}

// selfGuardsPath marks append as taking the per-path lock inside its own
// Execute. The recording wrapper (recording.go) must not hold the same lock
// across it: the lock is not reentrant per goroutine.
func (*AppendTool) selfGuardsPath() {}
