package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/jobs"
)

// oversee.go implements Plan 7 Phase 4 — `oversee`: retry-until-acceptable as a
// primitive, the composition Python Shepherd names `retry` with a `gate` inside
// (combinators/retry.py runs each attempt in a fresh fork and discards the
// failed ones; combinators/gating.py merges on the predicate, discards
// otherwise).
//
// One divergence, and it follows from Plan 7's split: the predicate is a
// SUB-AGENT's judgement, not a Python function. The orchestrator keeps the
// authority and the settlement; it delegates the judgement to a reviewer
// sub-agent. So the loop is: dispatch a retained worker, have a reviewer inspect
// its world, keep it (settle once) or discard it and re-dispatch with the reason
// as feedback, bounded by a round budget.
//
// Why this is not just "run, then undo": every round is a fresh worktree and a
// rejected round is DISCARDED, never merged — so the host tree is untouched
// until an accepted settle. That is the invariant the whole retention spine
// exists to provide.
type OverseeTool struct {
	// Runtime is the retained lane's wiring — the same one spawn_subagent uses.
	// Required: oversee is a composition over retained runs, so it exists only
	// where that lane does (it is registered alongside the proposal tool).
	Runtime *RetainedRuntime
}

func (*OverseeTool) Name() string { return "oversee" }

func (*OverseeTool) Description() string {
	return "Dispatch a sub-agent and retry it until a reviewer sub-agent accepts the work: each round runs in its own isolated worktree, the reviewer inspects it, and a rejected round is discarded (never merged) and re-dispatched with the reviewer's feedback. On acceptance the accepted proposal is settled and the change lands. Use it when a task must be *good*, not merely attempted; use spawn_subagent when you will do the review yourself."
}

func (*OverseeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"task": {
				"type": "string",
				"description": "The work to have done, stated as a complete instruction for the worker: what to change, where, and what done means."
			},
			"role": {
				"type": "string",
				"description": "The worker's role (default: developer). Must be a role with a write-capable tool set."
			},
			"reviewer": {
				"type": "string",
				"description": "The reviewing role (default: reviewer). Must be read-only; it inspects the worker's world and returns a verdict."
			},
			"max_rounds": {
				"type": "integer",
				"minimum": 1,
				"maximum": 10,
				"description": "Worker rounds before giving up (default 3). Each round is one worker dispatch plus one review dispatch."
			},
			"settle": {
				"type": "string",
				"enum": ["select", "apply", "none"],
				"description": "How to land an accepted proposal: select (default; fast-forward, fails closed if the tree moved), apply (three-way, path-disjoint), or none (hand the accepted proposal back to settle yourself with the proposal tool)."
			},
			"timeout_seconds": {
				"type": "integer",
				"description": "Per-dispatch timeout for both the worker and the reviewer. 0 means none."
			}
		},
		"required": ["task"]
	}`)
}

// DefaultOverseeRounds is the round budget when the caller does not set one:
// enough to act on one review, small enough that a reviewer who cannot be
// satisfied does not burn a session.
const DefaultOverseeRounds = 3

// verdictAccept is the only verdict that accepts work. Everything else — revise,
// reject, an unknown word, no verdict at all — is NOT acceptance: the loop
// retries and the round budget bounds it. Failing closed is the point, because
// the alternative is landing work a reviewer never approved.
const verdictAccept = "accept"

// overseeEnvelope is what the orchestrator receives. Status is "accepted"
// (the work landed), "proposed" (accepted, handed back unsettled), "exhausted"
// (no round was accepted; nothing landed), or "error".
type overseeEnvelope struct {
	Status     string   `json:"status"`
	Rounds     int      `json:"rounds"`
	Worker     string   `json:"worker_role"`
	Reviewer   string   `json:"reviewer_role"`
	ProposalID string   `json:"proposal_id,omitempty"`
	Action     string   `json:"action,omitempty"`
	Result     string   `json:"result,omitempty"`
	Review     string   `json:"review,omitempty"`
	Files      []string `json:"files,omitempty"`
	Next       []string `json:"next,omitempty"`
	Error      string   `json:"error,omitempty"`
}

func (t *OverseeTool) Execute(ctx context.Context, args string) (string, error) {
	var raw struct {
		Task           string `json:"task"`
		Role           string `json:"role"`
		Reviewer       string `json:"reviewer"`
		MaxRounds      int    `json:"max_rounds"`
		Settle         string `json:"settle"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return "", fmt.Errorf("oversee: invalid arguments: %w", err)
	}
	if strings.TrimSpace(raw.Task) == "" {
		return "", fmt.Errorf("oversee: task is required")
	}
	if t.Runtime == nil || t.Runtime.Runner == nil || t.Runtime.ScopeManager == nil || t.Runtime.RootScopeID == "" {
		return "", fmt.Errorf("oversee: the retained lane is not configured (set shepherd_trace_dir and agents.subagent.mode: retained)")
	}

	role := strings.TrimSpace(raw.Role)
	if role == "" {
		role = "developer"
	}
	reviewer := strings.TrimSpace(raw.Reviewer)
	if reviewer == "" {
		reviewer = "reviewer"
	}
	rounds := raw.MaxRounds
	if rounds <= 0 {
		rounds = DefaultOverseeRounds
	}
	if rounds > 10 {
		return "", fmt.Errorf("oversee: max_rounds %d is above the cap of 10", rounds)
	}
	settle := strings.TrimSpace(raw.Settle)
	if settle == "" {
		settle = "select"
	}
	switch settle {
	case "select", "apply", "none":
	default:
		return "", fmt.Errorf("oversee: settle must be select, apply, or none (got %q)", settle)
	}
	if settle != "none" {
		if _, ok := settlementVerb(settle); !ok {
			return "", fmt.Errorf("oversee: settle %q is not a settlement verb", settle)
		}
	}

	var timeout time.Duration
	if raw.TimeoutSeconds > 0 {
		timeout = time.Duration(raw.TimeoutSeconds) * time.Second
	}

	env := overseeEnvelope{Worker: role, Reviewer: reviewer, Next: []string{"proposal"}}
	var feedback string

	for round := 1; round <= rounds; round++ {
		env.Rounds = round

		prompt := strings.TrimSpace(raw.Task)
		if feedback != "" {
			prompt += "\n\n" + buildOverseeRetryBlock(round, feedback)
		}

		// 1. The worker: one retained dispatch, its own world, sealed proposal.
		runEnv, err := startRetainedRun(ctx, *t.Runtime, prompt, jobs.SubAgentParams{Role: role}, timeout)
		if err != nil {
			env.Status, env.Error = "error", err.Error()
			return marshalOverseeEnvelope(env), nil
		}
		var sealed proposalEnvelope
		if err := json.Unmarshal([]byte(runEnv), &sealed); err != nil {
			env.Status, env.Error = "error", "oversee: unreadable dispatch result: "+err.Error()
			return marshalOverseeEnvelope(env), nil
		}
		if sealed.Status != "proposal" {
			// Provisioning or sealing failed: there is nothing to review or
			// settle, so the round cannot be judged. Report it rather than
			// dispatching again into the same wall.
			env.Status, env.ProposalID = "error", sealed.ProposalID
			env.Error = strings.Join([]string{sealed.Error, sealed.Result}, "; ")
			return marshalOverseeEnvelope(env), nil
		}
		proposal, perr := getRetainedProposal(sealed.ProposalID)
		if perr != nil {
			env.Status, env.Error = "error", perr.Error()
			return marshalOverseeEnvelope(env), nil
		}
		env.ProposalID = proposal.id
		env.Result = proposal.result
		env.Files = changesPaths(proposal.output.Changes())

		// 2. The reviewer: a sub-agent inspecting the worker's world. It writes
		// nothing (read-only role, and the jail bounds it to that world anyway),
		// so it gets no proposal of its own.
		reviewText, rErr := runOverseeReview(ctx, t.Runtime, reviewer, proposal, raw.Task, round, rounds, timeout)
		if rErr != nil {
			// The review failed to run: the round is unjudged. Discard it —
			// leaving an unjudged proposal open would leak its worktree — and
			// report.
			_, _ = settleRetainedProposal(ctx, proposal.id, "discard")
			env.Status, env.Error = "error", rErr.Error()
			return marshalOverseeEnvelope(env), nil
		}
		verdict, reason := parseOverseeVerdict(reviewText)
		env.Review = reviewReason(verdict, reason)

		if verdict == verdictAccept {
			if settle == "none" {
				env.Status, env.Action = "proposed", "none"
				env.Next = proposalVerbs()
				return marshalOverseeEnvelope(env), nil
			}
			if _, serr := settleRetainedProposal(ctx, proposal.id, settle); serr != nil {
				// The proposal stays open: report so the orchestrator can
				// resolve the conflict (proposal show) instead of losing it.
				env.Status, env.Action = "error", settle
				env.Error = serr.Error()
				env.Next = proposalVerbs()
				return marshalOverseeEnvelope(env), nil
			}
			env.Status, env.Action = "accepted", settle
			return marshalOverseeEnvelope(env), nil
		}

		// 3. Not accepted: discard the round's world — its work never lands —
		// and carry the reason into the next dispatch.
		if _, derr := settleRetainedProposal(ctx, proposal.id, "discard"); derr != nil {
			env.Status, env.Error = "error", derr.Error()
			return marshalOverseeEnvelope(env), nil
		}
		if reason == "" {
			reason = strings.TrimSpace(reviewText)
		}
		feedback = reason
	}

	env.Status, env.Action = "exhausted", "discard"
	env.Next = []string{"spawn_subagent", "proposal"}
	return marshalOverseeEnvelope(env), nil
}

// runOverseeReview dispatches the reviewer against the worker's world. The
// reviewer's workdir IS the proposal's worktree (the post-change tree), so it
// inspects the actual result rather than a diff transcript — and because that
// worktree is already confined, Phase 3's jail bounds the review too.
func runOverseeReview(ctx context.Context, rt *RetainedRuntime, reviewer string, p *retainedProposal, task string, round, rounds int, timeout time.Duration) (string, error) {
	prompt := buildOverseeReviewPrompt(task, p, round, rounds)
	runCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return rt.Runner(runCtx, prompt, jobs.SubAgentParams{Role: reviewer, Workdir: p.workdir})
}

// buildOverseeReviewPrompt states the task, the worker's report, and the
// verdict contract the loop parses. The reviewer is told to END with the verdict
// because the role's own response contract also asks for a trailing block —
// naming the order removes the ambiguity, and the parser reads the last verdict
// regardless.
func buildOverseeReviewPrompt(task string, p *retainedProposal, round, rounds int) string {
	var b strings.Builder
	b.WriteString("A sub-agent has finished a piece of work and it is now yours to judge. ")
	b.WriteString("Your working directory IS that sub-agent's tree, with its changes applied: read the files there to check the work in context.\n\n")
	b.WriteString("## The task it was given\n\n")
	b.WriteString(strings.TrimSpace(task))
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "## Its report (round %d of %d)\n\n", round, rounds)
	b.WriteString(strings.TrimSpace(p.result))
	if p.runErr != "" {
		b.WriteString("\n\nIt also reported an error: " + p.runErr)
	}
	if p.output != nil {
		if files := changesPaths(p.output.Changes()); len(files) > 0 {
			b.WriteString("\n\nFiles changed:\n")
			for _, f := range files {
				b.WriteString("- " + f + "\n")
			}
		}
		if diff := clipDiff(p.output.Diff()); strings.TrimSpace(diff) != "" {
			b.WriteString("\nThe change as a diff:\n\n```diff\n" + diff + "\n```\n")
		}
	}
	b.WriteString("\n## Your verdict\n\n")
	b.WriteString("Decide whether the work satisfies the task — correctness and completeness against what was asked, not style preference. ")
	b.WriteString("Read the tree before deciding; do not accept work you have not looked at.\n\n")
	b.WriteString("Override your usual response contract for this reply: it must END with exactly one of these, with nothing after it:\n\n")
	b.WriteString("VERDICT: accept\n\n")
	b.WriteString("VERDICT: revise\nREASON: <what must change, addressed to the sub-agent that did the work>\n")
	return b.String()
}

// buildOverseeRetryBlock is the feedback the next worker round receives: the
// reviewer's reason, framed the way the old supervised loop framed its guidance.
func buildOverseeRetryBlock(round int, reason string) string {
	return fmt.Sprintf("--- REVIEW GUIDANCE (this is attempt %d; the previous attempt was rejected) ---\n"+
		"A reviewer inspected your previous attempt in its own copy of the tree and rejected it:\n\n%s\n\n"+
		"Address exactly this. The previous attempt was discarded, so start from the current state of the tree — "+
		"do not assume any of it is present.", round, reason)
}

// parseOverseeVerdict reads the LAST verdict in the reply (the reviewer may
// reason before deciding) and the reason that follows it. Anything unrecognized
// returns "" for the verdict — not acceptance.
func parseOverseeVerdict(review string) (verdict, reason string) {
	lines := strings.Split(review, "\n")
	idx := -1
	for i, ln := range lines {
		if w, ok := verdictWord(ln); ok {
			idx, verdict = i, w
		}
	}
	if idx < 0 {
		return "", ""
	}
	var parts []string
	for _, ln := range lines[idx+1:] {
		trimmed := strings.TrimSpace(ln)
		// Stop at a trailing contract/diff block rather than swallowing it as
		// feedback.
		if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "```") {
			break
		}
		if trimmed == "" {
			continue
		}
		if k, v, ok := strings.Cut(trimmed, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "reason") {
			parts = append(parts, strings.TrimSpace(v))
			continue
		}
		parts = append(parts, trimmed)
	}
	return verdict, strings.TrimSpace(strings.Join(parts, "\n"))
}

// verdictWord recognizes a `VERDICT: <word>` line, tolerating the emphasis
// (bold, backticks, a leading list bullet) a model tends to add.
func verdictWord(line string) (string, bool) {
	line = strings.Trim(strings.TrimSpace(line), "*_`#")
	line = strings.TrimSpace(strings.TrimLeft(line, "-• "))
	const prefix = "VERDICT:"
	if len(line) < len(prefix) || !strings.EqualFold(line[:len(prefix)], prefix) {
		return "", false
	}
	word := strings.Trim(strings.TrimSpace(line[len(prefix):]), "*_`.\"'")
	if word == "" {
		return "", false
	}
	return strings.ToLower(word), true
}

// reviewReason renders the verdict for the envelope.
func reviewReason(verdict, reason string) string {
	switch {
	case verdict == "":
		return "no verdict — treated as not accepted"
	case reason == "":
		return verdict
	default:
		return verdict + ": " + reason
	}
}

func marshalOverseeEnvelope(env overseeEnvelope) string {
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Sprintf(`{"status":"error","error":%q}`, err.Error())
	}
	return string(data)
}
