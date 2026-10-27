package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// ProposalTool settles retained sub-agent proposals (Plan 7 Phase 2). A retained
// dispatch (delegate in "retained" mode) runs a sub-agent in an isolated
// worktree and seals its delta into a proposal; the orchestrator reads it and
// settles it once — select (fast-forward), apply (three-way, path-disjoint),
// release (export the delta, then tear the worktree down), or discard (throw it
// away and remove its worktree). The verbs mirror Shepherd's run select | apply
// | release | discard.
type ProposalTool struct {
	// Proposals is the session's registry of open proposals, created by
	// runner.NewDelegateTool alongside the retained runtime and passed to this
	// tool at wiring. A session can only ever list, show, or settle its own
	// proposals (yaah-yox). Required: without it the tool refuses to execute
	// rather than falling back to a shared registry.
	Proposals *ProposalRegistry
}

func (*ProposalTool) Name() string { return "proposal" }

func (*ProposalTool) Description() string {
	return "Settle retained sub-agent proposals: select (fast-forward), apply (three-way merge, path-disjoint), release, discard — plus list and show. A proposal is produced by delegate."
}

func (*ProposalTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["list", "show", "select", "apply", "release", "discard"],
				"description": "list: show open proposals; show: one proposal's diff and files; select: fast-forward the proposal onto the working tree — fails closed if the tree moved since the dispatch; apply: three-way merge onto a moved tree, path-disjoint or refused; release: export the delta to a retention path, then tear the worktree down; discard: throw the proposal away and remove its worktree"
			},
			"proposal_id": {
				"type": "string",
				"description": "The proposal id from a delegate result (required for show, select, apply, release, discard)"
			}
		},
		"required": ["action"]
	}`)
}

func (t *ProposalTool) Execute(ctx context.Context, args string) (string, error) {
	var raw struct {
		Action     string `json:"action"`
		ProposalID string `json:"proposal_id"`
	}
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return "", fmt.Errorf("proposal: invalid arguments: %w", err)
	}
	if t.Proposals == nil {
		return "", fmt.Errorf("proposal: no session registry configured — this tool is registered with the registry runner.NewDelegateTool creates")
	}

	switch raw.Action {
	case "list":
		return t.Proposals.listEnvelope(), nil
	case "show":
		if raw.ProposalID == "" {
			return "", fmt.Errorf("proposal: proposal_id required for show")
		}
		p, err := t.Proposals.get(raw.ProposalID)
		if err != nil {
			return "", err
		}
		env := proposalEnvelope{
			Status:     "proposal",
			ProposalID: p.id,
			Result:     p.result,
			Error:      p.runErr,
			Next:       proposalVerbs(),
		}
		if p.output != nil {
			env.Diff = clipDiff(p.output.Diff())
			env.Files = changesPaths(p.output.Changes())
		}
		return marshalProposalEnvelope(env), nil
	case "select", "apply", "release", "discard":
		if raw.ProposalID == "" {
			return "", fmt.Errorf("proposal: proposal_id required for %s", raw.Action)
		}
		return t.Proposals.settleRetainedProposal(ctx, raw.ProposalID, raw.Action)
	default:
		return "", fmt.Errorf("proposal: unknown action %q (list, show, select, apply, release, discard)", raw.Action)
	}
}
