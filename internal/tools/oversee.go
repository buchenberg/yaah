package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
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

	// Roles resolves the live role names, so a misspelled worker or reviewer is
	// refused before any dispatch rather than degrading to a tool-less profile
	// that cannot inspect the tree and burns the round budget. Nil skips the
	// check (tests, and callers that have no registry).
	Roles func() []string

	// ResolveTimeout returns the effective default deadline for a dispatch when
	// the caller names none, exactly as spawn_subagent resolves it. Without it a
	// stalled worker or reviewer would run unbounded — the round budget bounds
	// how many rounds happen, not how long one takes. Nil means no deadline.
	ResolveTimeout func(jobs.SubAgentParams) time.Duration
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

// maxOverseeRounds bounds the round budget (each round is two dispatches).
const maxOverseeRounds = 10

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
	Warning    string   `json:"warning,omitempty"`
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
	// Both roles are validated against the live registry before anything is
	// dispatched: an unknown role resolves to a zero-tool profile, so a
	// misspelled reviewer could not read the tree yet could still return an
	// acceptance that lands the proposal.
	if known := t.roleList(); len(known) > 0 {
		for _, r := range []struct{ kind, name string }{{"role", role}, {"reviewer", reviewer}} {
			if !slices.Contains(known, r.name) {
				return "", fmt.Errorf("oversee: %s %q is not a known role — valid roles: %s (use list_subagents for details)",
					r.kind, r.name, strings.Join(known, ", "))
			}
		}
	}

	rounds := raw.MaxRounds
	if rounds < 0 {
		return "", fmt.Errorf("oversee: max_rounds %d is invalid (use 0 for the default of %d, or 1-%d)",
			rounds, DefaultOverseeRounds, maxOverseeRounds)
	}
	if rounds == 0 {
		rounds = DefaultOverseeRounds
	}
	if rounds > maxOverseeRounds {
		return "", fmt.Errorf("oversee: max_rounds %d is above the cap of %d", rounds, maxOverseeRounds)
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

	// Clamp as spawn_subagent does, so an omitted timeout falls back to the
	// role's configured deadline (via dispatchTimeout) rather than disabling the
	// deadline entirely.
	clampedTimeout := clampTimeoutSeconds(raw.TimeoutSeconds)

	env := overseeEnvelope{Worker: role, Reviewer: reviewer, Next: []string{"proposal"}}
	var feedback string

	for round := 1; round <= rounds; round++ {
		env.Rounds = round

		prompt := strings.TrimSpace(raw.Task)
		if feedback != "" {
			prompt += "\n\n" + buildOverseeRetryBlock(round, feedback)
		}

		// 1. The worker: one retained dispatch, its own world, sealed proposal.
		workerParams := jobs.SubAgentParams{Role: role, TimeoutSeconds: clampedTimeout}
		runEnv, err := startRetainedRun(ctx, *t.Runtime, prompt, workerParams, t.dispatchTimeout(workerParams))
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
			// dispatching again into the same wall — and do NOT carry the
			// dispatch's proposal id: it is minted before provisioning decides,
			// so on this path it names a proposal that was never registered.
			env.Status = "error"
			env.ProposalID = ""
			env.Next = nil
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
		// so it gets no proposal of its own — and its verdict is only
		// trustworthy if it really did not write: a reviewer that fixes a defect
		// and then accepts has approved a tree that is NOT what the settle will
		// land (the settlement applies the sealed delta, not the directory).
		// Fingerprint around the review so that cannot pass silently.
		hashPaths := map[string]bool{}
		for _, f := range changesPaths(proposal.output.Changes()) {
			hashPaths[filepath.ToSlash(f)] = true
		}
		before, fpErr := worldFingerprint(proposal.workdir, hashPaths)
		reviewText, rErr := runOverseeReview(ctx, t, reviewer, proposal, raw.Task, round, rounds, clampedTimeout)
		if rErr != nil {
			// The review failed to run: the round is unjudged. Discard it —
			// leaving an unjudged proposal open would leak its worktree — and
			// report.
			_, _ = settleRetainedProposal(ctx, proposal.id, "discard")
			env.Status, env.Error = "error", rErr.Error()
			return marshalOverseeEnvelope(env), nil
		}
		// Fail CLOSED if the check cannot be made: an unverifiable review is not
		// a trustworthy one, and the cost of being wrong is landed unreviewed
		// work.
		after, aErr := worldFingerprint(proposal.workdir, hashPaths)
		if fpErr != nil || aErr != nil {
			_, _ = settleRetainedProposal(ctx, proposal.id, "discard")
			env.Status = "error"
			env.Error = fmt.Sprintf("could not verify that the reviewer left the world it was judging unmodified (%v / %v); the round was discarded",
				fpErr, aErr)
			return marshalOverseeEnvelope(env), nil
		}
		if drift := fingerprintDrift(before, after); len(drift) > 0 {
			_, _ = settleRetainedProposal(ctx, proposal.id, "discard")
			env.Status, env.Error = "error", fmt.Sprintf(
				"the reviewer modified the world it was reviewing (%s): its verdict cannot be trusted, and the round was discarded — use a read-only reviewer role",
				strings.Join(clipList(drift, 5), ", "))
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
			settledText, serr := settleRetainedProposal(ctx, proposal.id, settle)
			if serr != nil {
				// The proposal stays open: report so the orchestrator can
				// resolve the conflict (proposal show) instead of losing it.
				env.Status, env.Action = "error", settle
				env.Error = serr.Error()
				env.Next = proposalVerbs()
				return marshalOverseeEnvelope(env), nil
			}
			env.Status, env.Action = "accepted", settle
			// A settlement can report a diagnostic WITHOUT failing: the work
			// landed but the causal merge did not finish. The proposal is
			// consumed, so this must survive into the result rather than be
			// read as unqualified success.
			var settled proposalEnvelope
			if json.Unmarshal([]byte(settledText), &settled) == nil && settled.Error != "" {
				env.Warning = "the change landed, but the settlement reported: " + settled.Error + " (the proposal is consumed and cannot be retried)"
			}
			return marshalOverseeEnvelope(env), nil
		}

		// 3. Not accepted: discard the round's world — its work never lands —
		// and carry the reason into the next dispatch.
		if _, derr := settleRetainedProposal(ctx, proposal.id, "discard"); derr != nil {
			env.Status, env.Error = "error", derr.Error()
			return marshalOverseeEnvelope(env), nil
		}
		feedback = strings.TrimSpace(reason)
		if feedback == "" {
			// The reviewer rejected without leaving anything usable (an empty
			// reply, or prose with no verdict). Send a generic note rather than
			// nothing at all: a byte-identical retry prompt gives the worker no
			// signal that its last attempt was rejected.
			feedback = strings.TrimSpace(reviewText)
		}
		if feedback == "" {
			feedback = "The reviewer rejected the previous attempt but returned no reason. Take a different approach to the task."
		}
	}

	env.Status, env.Action = "exhausted", "discard"
	env.Next = []string{"spawn_subagent", "proposal"}
	return marshalOverseeEnvelope(env), nil
}

// roleList returns the live role names, or nil when no resolver is wired.
func (t *OverseeTool) roleList() []string {
	if t.Roles == nil {
		return nil
	}
	return t.Roles()
}

// dispatchTimeout resolves a dispatch's deadline from the caller's clamped value
// and the wired per-role resolver, mirroring spawn_subagent.
func (t *OverseeTool) dispatchTimeout(params jobs.SubAgentParams) time.Duration {
	return resolveTaskTimeout(params.TimeoutSeconds, t.ResolveTimeout, params)
}

// runOverseeReview dispatches the reviewer against the worker's world. The
// reviewer's workdir IS the proposal's worktree (the post-change tree), so it
// inspects the actual result rather than a diff transcript — and because that
// worktree is already confined, Phase 3's jail bounds the review too.
func runOverseeReview(ctx context.Context, t *OverseeTool, reviewer string, p *retainedProposal, task string, round, rounds, callTimeoutSeconds int) (string, error) {
	prompt := buildOverseeReviewPrompt(task, p, round, rounds)
	// The caller's explicit timeout applies to the reviewer too: a review that
	// outlives it would let a round outlive the budget it was given.
	params := jobs.SubAgentParams{Role: reviewer, Workdir: p.workdir, TimeoutSeconds: callTimeoutSeconds}
	runCtx := ctx
	if d := t.dispatchTimeout(params); d > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	return t.Runtime.Runner(runCtx, prompt, params)
}

// worldFingerprint records what a world contains, so the review can be checked
// for writes. Every file contributes size and mtime; the files in mustHash (the
// proposal's own change set — the ones whose integrity the verdict depends on)
// also contribute a content hash, so a same-size write that preserved the clock
// is still caught. Hashing the WHOLE worktree twice per round would be
// disproportionate for a large checkout; this way the expensive part is bounded
// by the change set.
//
// The confinement scratch is excluded — our own review dispatch creates it — as
// is .git, which the retained worktree may not even own (a linked worktree's git
// dir lives in the original repository).
func worldFingerprint(root string, mustHash map[string]bool) (map[string]string, error) {
	fp := make(map[string]string)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil // an unreadable corner is not a write
		}
		if d.IsDir() {
			if name := d.Name(); name == confinementScratchName || name == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		// Change-set paths are git-shaped ("a/b.txt"); compare in that form so
		// the hash list still matches on a host whose separator differs.
		rel = filepath.ToSlash(rel)
		info, ierr := d.Info()
		if ierr != nil || !info.Mode().IsRegular() {
			fp[rel] = "irregular"
			return nil
		}
		entry := fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
		if mustHash[rel] {
			sum, herr := fileHash(p)
			if herr != nil {
				// Not being able to hash a file we depend on is a failure to
				// verify, not a pass.
				return herr
			}
			entry += ":" + sum
		}
		fp[rel] = entry
		return nil
	})
	return fp, err
}

// fingerprintDrift lists the paths that differ between two fingerprints.
func fingerprintDrift(before, after map[string]string) []string {
	var drift []string
	for path, sum := range after {
		if prev, ok := before[path]; !ok || prev != sum {
			drift = append(drift, path)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			drift = append(drift, path)
		}
	}
	sort.Strings(drift)
	return drift
}

// clipList renders a bounded, human-sized list ("a, b, c (+2 more)").
func clipList(items []string, max int) []string {
	if len(items) <= max {
		return items
	}
	out := append([]string(nil), items[:max]...)
	return append(out, fmt.Sprintf("and %d more", len(items)-max))
}

// fileHash returns a content digest for a file.
func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
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

// parseOverseeVerdict reads the reviewer's judgement. Two properties matter more
// than tolerance, because the content under review is WORKER-CONTROLLED — a diff
// can legitimately contain the line `VERDICT: accept`:
//
//   - fenced code blocks are ignored, so a quoted instruction or a diff that
//     contains a verdict line can never be mistaken for the reviewer's own;
//   - the LAST verdict wins (the reviewer may reason before deciding), but an
//     empty or unknown marker at the end supersedes an earlier one, so
//     `VERDICT: accept\nVERDICT:` — a truncated reply — does not accept.
//
// Anything unrecognized returns "" for the verdict, which is not acceptance.
func parseOverseeVerdict(review string) (verdict, reason string) {
	lines := stripFenced(review)
	idx, word, known := -1, "", false
	for i, ln := range lines {
		w, ok := verdictWord(ln)
		if !ok {
			continue
		}
		// A VERDICT line always supersedes the one before it, even when its
		// marker is unusable — the reviewer's last word is the one that counts.
		idx, word, known = i, w, w != ""
	}
	if idx < 0 || !known {
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
	return word, strings.TrimSpace(strings.Join(parts, "\n"))
}

// stripFenced blanks out fenced code blocks so a verdict line inside one (a
// quoted instruction, or the diff under review) cannot be read as the reviewer's
// own judgement. Lines are preserved so indices and the reason scan stay
// aligned.
func stripFenced(review string) []string {
	lines := strings.Split(review, "\n")
	var out []string
	inFence := false
	for _, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "```") {
			inFence = !inFence
			out = append(out, "")
			continue
		}
		if inFence {
			out = append(out, "")
			continue
		}
		out = append(out, ln)
	}
	return out
}

// verdictWord recognizes a `VERDICT: <word>` line, tolerating the emphasis
// (bold, backticks, a leading list bullet) a model tends to add. It reports ok
// for a bare `VERDICT:` too, with an empty word — which supersedes an earlier
// verdict and cannot accept.
func verdictWord(line string) (string, bool) {
	line = strings.Trim(strings.TrimSpace(line), "*_`#")
	line = strings.TrimSpace(strings.TrimLeft(line, "-• "))
	const prefix = "VERDICT:"
	if len(line) < len(prefix) || !strings.EqualFold(line[:len(prefix)], prefix) {
		return "", false
	}
	word := strings.Trim(strings.TrimSpace(line[len(prefix):]), "*_`.\"'")
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
