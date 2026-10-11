package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

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
// The approval prompt shows only the tool name and arguments, not the
// delta — so Execute refuses an apply that was not preceded by a dry run
// in this session, and refuses one whose preview has gone stale: the user
// approves exactly the diff they were shown, or nothing. A mutation that
// lands in the microseconds between the staleness check and the backend's
// own capture is the residual window; closing it needs backend-level
// capture-and-apply atomicity, which is settlement's problem, not this
// tool's.
//
// It is deliberately NOT a filesystem tool: it must not be routed through
// the sandbox workspace it is the door out of, and it must not carry the
// hostOnly() marker either — it is the one tool whose whole job is to
// write the host, registered only in isolated mode.

// ApplyOutTool applies the session sandbox's outward delta onto the host
// working tree, or previews it.
type ApplyOutTool struct {
	// Sandbox is the session's sandbox; its baseline (from the D1
	// bootstrap) is the anchor the delta is computed against.
	Sandbox *SessionSandbox
	// HostDir is the host working tree the delta lands in — the source
	// the bootstrap copied in.
	HostDir string

	// mu serializes this tool's calls and guards preview. Tool calls can
	// dispatch in parallel, so two apply_out calls — or a dry run racing
	// an apply — must not interleave.
	mu sync.Mutex
	// preview is the exact diff text of the last dry run; "" when no
	// preview has been shown in this session.
	preview string
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
// The diff is unbounded on purpose: it is both what the user sees and the
// token the apply is verified against, so truncating it would let two
// different workspaces compare equal past the cut. Oversized results are
// the pipeline's to spill.
func (t *ApplyOutTool) Execute(ctx context.Context, args string) (string, error) {
	var p applyOutParams
	if args != "" {
		if err := json.Unmarshal([]byte(args), &p); err != nil {
			return "", fmt.Errorf("apply_out: parse arguments: %w", err)
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	diff, files, err := t.Sandbox.DiffSinceBaseline(ctx, 0)
	if err != nil {
		return "", fmt.Errorf("apply_out: %w", err)
	}
	if len(files) == 0 {
		t.preview = "" // nothing to apply, so nothing is approved either
		return "No changes in the isolated workspace — the host tree and the container match.", nil
	}

	sort.Strings(files)
	if p.DryRun {
		t.preview = diff
		return fmt.Sprintf("Dry run — %d file(s) would be applied to the host working tree; the host is untouched.\n\n%s",
			len(files), diff), nil
	}

	// The approval prompt shows only "apply_out" and its arguments — the
	// user must have seen the delta somewhere, and the only somewhere is
	// a dry run's result in this session.
	if t.preview == "" {
		return "", fmt.Errorf("apply_out: no preview yet — call with dry_run: true first and show the user the diff; the approval prompt cannot show it")
	}
	if t.preview != diff {
		return "", fmt.Errorf("apply_out: the workspace changed since the preview — call with dry_run: true again and show the user the updated diff")
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
