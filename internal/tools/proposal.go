package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/prompts"
)

// ProposalTool settles retained sub-agent proposals (Plan 7 Phase 2). A retained
// dispatch runs a sub-agent in its bound world — an isolated git worktree, or a
// per-dispatch container — and seals the work into a proposal; the orchestrator
// reads it and settles it once. A worktree proposal settles through the kernel's
// state-based verbs: select (fast-forward), apply (three-way, path-disjoint),
// release (export the delta, then tear the worktree down), or discard (throw it
// away and remove its worktree). A container proposal settles as a GitDelta
// manifest: select (fast-forward onto the dispatch basis), apply (whole-path,
// path-disjoint), release (export the manifest), or discard. The verbs mirror
// Shepherd's run select | apply | release | discard.
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
	return prompts.ToolDescription("proposal")
}

func (*ProposalTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["list", "show", "select", "apply", "release", "discard"],
				"description": "list: show open proposals, their parent drift, and typed content-aware conflicts; show: one proposal's diff, files, changes, and conflicts; select: fast-forward onto the dispatch basis — fails closed if the basis moved since the dispatch; apply: apply path-disjoint onto a moved tree (a worktree proposal merges three-way, a container proposal writes whole-path post-images), refused on overlap with a machine-readable conflict; release: export the delta to a retention path (and tear a worktree down; a container world already ended at seal); discard: throw the proposal away"
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
		return t.Proposals.listEnvelope(ctx), nil
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
		if p.delta != nil {
			// A container proposal's world is gone; the manifest is the
			// proposal, and its review was captured with it.
			env.World = string(subagent.WorldContainer)
			env.Target = p.target
			if env.Target == "" {
				env.Target = "host"
			}
		}
		env.Diff = clipDiff(p.reviewDiff())
		env.Files = p.changedFiles()

		// show carries the same typed conflict view as list, filtered to this
		// proposal. It also records any newly observed conflict fact, so a
		// caller that inspects one proposal still leaves the evidence behind.
		analyses := t.Proposals.analyzeProposals(ctx)
		conflicts := proposalConflicts(analyses)
		t.Proposals.recordConflictFacts(ctx, analyses, conflicts)
		for _, analysis := range analyses {
			if analysis.proposal.id != p.id {
				continue
			}
			env.Changes = analysis.changes
			if analysis.changesErr != nil {
				env.ChangesError = analysis.changesErr.Error()
			}
			break
		}
		env.Conflicts = conflictsWith(conflicts, p.id)
		return marshalProposalEnvelope(env), nil
	case "select", "apply", "release", "discard":
		if raw.ProposalID == "" {
			return "", fmt.Errorf("proposal: proposal_id required for %s", raw.Action)
		}
		out, err := t.Proposals.settleRetainedProposal(ctx, raw.ProposalID, raw.Action)
		if err != nil {
			var conflict *proposalApplyConflict
			if raw.Action == "apply" && errors.As(err, &conflict) {
				// A refusal is an outcome the orchestrator must act on, not a
				// failed tool call: return the same JSON frame as a settlement,
				// with the overlap paths named. The proposal stays open.
				paths := conflict.Paths
				if paths == nil {
					paths = []string{}
				}
				env := proposalEnvelope{
					Status:     "conflict",
					ProposalID: conflict.ProposalID,
					Action:     raw.Action,
					Error:      conflict.Error(),
					Conflicts: []proposalConflict{{
						Kind:      proposalConflictApply,
						Paths:     paths,
						Proposals: []string{conflict.ProposalID},
						Changes:   conflict.Changes,
					}},
					Next: proposalVerbs(),
				}
				return marshalProposalEnvelope(env), nil
			}
			return "", err
		}
		return out, nil
	default:
		return "", fmt.Errorf("proposal: unknown action %q (list, show, select, apply, release, discard)", raw.Action)
	}
}
