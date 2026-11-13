package dispatch

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/toolargs"
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
	// Runtime is the session's retained runtime (runner + scopes), needed to
	// re-run a proposal for continue/fork. Nil leaves settlement available and
	// refuses continue/fork rather than guessing a runner.
	Runtime *RetainedRuntime
}

// Continuation budgets. maxContinueTurns matches the delegate tool's max_turns
// maximum so a continuation is bounded by the same budget model as a fresh
// dispatch; defaultContinueTurns applies when a caller omits extra_turns.
const (
	defaultContinueTurns = 5
	maxContinueTurns     = 50
)

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
				"enum": ["list", "show", "select", "apply", "release", "discard", "continue", "fork"],
				"description": "list: show open proposals, their parent drift, and typed content-aware conflicts; show: one proposal's diff, files, changes, and conflicts; select: fast-forward onto the dispatch basis — fails closed if the basis moved since the dispatch; apply: apply path-disjoint onto a moved tree (a worktree proposal merges three-way, a container proposal writes whole-path post-images), refused on overlap with a machine-readable conflict — content-checked against the dispatch basis before any write, so a parent edit to a proposal-owned path (made after the dispatch) never gets overwritten whole-path; release: export the delta to a retention path (and tear a worktree down; a container world already ended at seal); discard: throw the proposal away; continue: re-run a turn-exhausted worktree proposal with extra_turns more turns, seeded from its captured conversation and sealed workspace, and seal the result as a new superseding proposal; fork: the same re-run but leaves the original open for comparison"
			},
			"proposal_id": {
				"type": "string",
				"description": "The proposal id from a delegate result (required for show, select, apply, release, discard, continue, fork)"
			},
			"strategy": {
				"type": "string",
				"enum": ["path_disjoint", "three_way"],
				"description": "apply only: path_disjoint (default; refuses any parent overlap) or three_way (worktree lane only; explicit Git merge, fails closed on a textual conflict)"
			},
			"extra_turns": {
				"type": "integer",
				"minimum": 1,
				"maximum": 50,
				"default": 5,
				"description": "continue/fork only: how many additional tool turns to grant the continuation (default 5, max 50)"
			},
			"prompt": {
				"type": "string",
				"description": "continue/fork only: optional instruction for the continuation; defaults to a supervisor wrap-up prompt"
			}
		},
		"required": ["action"]
	}`)
}

// IsDangerous reports whether the action settles or extends a proposal.
// list and show only read.
func (t *ProposalTool) IsDangerous(argsJSON string) bool {
	var params struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &params); err != nil {
		return true
	}
	switch params.Action {
	case "list", "show":
		return false
	default:
		return true
	}
}

func (t *ProposalTool) Execute(ctx context.Context, args string) (string, error) {
	var raw struct {
		Action     string `json:"action"`
		ProposalID string `json:"proposal_id"`
		Strategy   string `json:"strategy"`
		ExtraTurns int    `json:"extra_turns"`
		Prompt     string `json:"prompt"`
	}
	if err := toolargs.StrictToolArgs("proposal", t.Schema(), args, &raw); err != nil {
		return "", err
	}
	if raw.Strategy != "" && raw.Action != "apply" {
		return "", fmt.Errorf("proposal: strategy is only valid with apply")
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
			StopReason: p.stopReason,
			Next:       nextVerbsFor(p),
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
		conflicts, duplicates := proposalConflicts(analyses)
		conflicts = attachProposalConflictDetails(ctx, conflicts, analyses)
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
		env.Duplicates = conflictsWith(duplicates, p.id)
		return marshalProposalEnvelope(env), nil
	case "select", "apply", "release", "discard":
		if raw.ProposalID == "" {
			return "", fmt.Errorf("proposal: proposal_id required for %s", raw.Action)
		}
		settlement, err := Settle(ctx, t.Proposals, raw.ProposalID, SettleVerb(raw.Action), SettleOpts{Strategy: raw.Strategy})
		if err != nil {
			return "", err
		}
		return marshalProposalEnvelope(settlement.Envelope()), nil
	case "continue", "fork":
		if raw.ProposalID == "" {
			return "", fmt.Errorf("proposal: proposal_id required for %s", raw.Action)
		}
		if t.Runtime == nil {
			return "", fmt.Errorf("proposal: %s needs the session retained runtime — runner.NewDelegateTool wires it", raw.Action)
		}
		extraTurns := raw.ExtraTurns
		if extraTurns <= 0 {
			extraTurns = defaultContinueTurns
		}
		return ContinueRetainedRun(ctx, *t.Runtime, raw.ProposalID, extraTurns, raw.Prompt, raw.Action == "continue")
	default:
		return "", fmt.Errorf("proposal: unknown action %q (list, show, select, apply, release, discard, continue, fork)", raw.Action)
	}
}
