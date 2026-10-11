package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

// apply_out.go is the accept seam of the isolated workspace (plan §2.3,
// decision D1): copy-in at session start, capture/diff during, apply-out
// on accept. The bootstrap verbs landed with the D1 step; this tool is the
// session surface that finally invokes ApplyOut, so changes do not leave
// the container until a human accepts them.
//
// The accept decision belongs to the user, not the model: the tool
// implements DangerClassifier, so the actual apply is gated behind the
// approval flow (approval mode "ask" prompts; "deny" strips the call). A
// dry run is never dangerous — the model can always show the user what
// would land before asking them to accept it.
//
// It is deliberately NOT a filesystem tool: it must not be routed through
// the sandbox workspace it is the door out of, and it must not carry the
// hostOnly() marker either — it is the one tool whose whole job is to
// write the host, registered only in isolated mode.

// applyOutDiffMaxLines bounds the diff this tool returns, so a huge
// uncommitted change cannot turn the tool result into a wall of text; the
// inlinelimit middleware would spill it either way, but bounding at the
// source keeps the useful summary readable above the fold.
const applyOutDiffMaxLines = 500

// ApplyOutTool applies the session sandbox's outward delta onto the host
// working tree, or previews it.
type ApplyOutTool struct {
	// Sandbox is the session's sandbox; its baseline (from the D1
	// bootstrap) is the anchor the delta is computed against.
	Sandbox *SessionSandbox
	// HostDir is the host working tree the delta lands in — the source
	// the bootstrap copied in.
	HostDir string
}

func (t *ApplyOutTool) Name() string        { return "apply_out" }
func (t *ApplyOutTool) Description() string { return prompts.ToolDescription("apply_out") }

func (t *ApplyOutTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"dry_run": {
				"type": "boolean",
				"description": "Preview the diff that would be applied without touching the host (default false)"
			}
		}
	}`)
}

type applyOutParams struct {
	DryRun bool `json:"dry_run"`
}

// Execute previews (dry_run) or applies the session's delta onto the host.
func (t *ApplyOutTool) Execute(ctx context.Context, args string) (string, error) {
	var p applyOutParams
	if args != "" {
		if err := json.Unmarshal([]byte(args), &p); err != nil {
			return "", fmt.Errorf("apply_out: parse arguments: %w", err)
		}
	}

	diff, files, err := t.Sandbox.DiffSinceBaseline(ctx, applyOutDiffMaxLines)
	if err != nil {
		return "", fmt.Errorf("apply_out: %w", err)
	}
	if len(files) == 0 {
		return "No changes in the isolated workspace — the host tree and the container match.", nil
	}

	sort.Strings(files)
	if p.DryRun {
		return fmt.Sprintf("Dry run — %d file(s) would be applied to the host working tree; the host is untouched.\n\n%s",
			len(files), diff), nil
	}
	if err := t.Sandbox.ApplyOut(ctx, t.HostDir); err != nil {
		return "", fmt.Errorf("apply_out: %w", err)
	}
	return fmt.Sprintf("Applied %d file(s) onto the host working tree:\n  %s\n\nThe changes are in the host's working tree — not committed. Review and commit with the user's normal git flow.",
		len(files), strings.Join(files, "\n  ")), nil
}

// IsDangerous gates the real apply behind user approval. A dry run never
// touches the host, so it is never dangerous; unparseable arguments are
// treated as an apply — failing closed on the approval gate.
func (t *ApplyOutTool) IsDangerous(argsJSON string) bool {
	var p applyOutParams
	if argsJSON == "" {
		return true
	}
	if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
		return true
	}
	return !p.DryRun
}
