package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	shepherdtrace "github.com/buchenberg/shepherd-kernel-go"
	shepherd "github.com/buchenberg/shepherd-runtime-go"
	shepherdsandbox "github.com/buchenberg/shepherd-runtime-go/sandbox"
	shepherdgit "github.com/buchenberg/shepherd-runtime-go/sandbox/git"
	"github.com/buchenberg/yaah/internal/agent/recovery"
	"github.com/buchenberg/yaah/internal/jobs"
)

// retained.go implements the retained-run primitive (Plan 7 Phase 2): a
// sub-agent dispatch that runs inside an isolated worktree — its bound world —
// seals its delta as a retained output, and hands the sealed proposal back to
// the orchestrator, which settles it exactly once. The parent tree is untouched
// until a settlement; the orchestrator's own host path is never involved.
//
// This is the default dispatch lane ("retained"). agents.subagent.mode: shared
// keeps the older direct-to-host behaviour for a migration or a benchmark
// baseline.

// RetainedRuntime carries the wiring a retained dispatch needs. It is injected
// by the composition root.
type RetainedRuntime struct {
	// Runner is the sub-agent execution closure (the same one spawn_subagent
	// uses). Required.
	Runner jobs.TaskRunner
	// RepoPath is the git repository the worktree is cut from.
	RepoPath string
	// ScopeManager is the session's scope registry. Required: a retained run
	// forks an isolated scope through it.
	ScopeManager *shepherd.ScopeManager
	// RootScopeID is the session's root scope — the fork parent, and the merge
	// target a settlement applies to. Required.
	RootScopeID string
	// WorktreeRoot is the parent directory for retained worktrees. Empty
	// defaults to "shepherd-worktrees" beside the repository.
	WorktreeRoot string
	// WorktreeBootstrap runs inside each worktree after creation, for repos
	// whose build needs gitignored artifacts.
	WorktreeBootstrap string
}

// proposalEnvelope is the JSON the orchestrator receives from a retained
// dispatch and from a settlement. Status is "proposal" (awaiting a verdict),
// "settled", or "error".
type proposalEnvelope struct {
	Status     string   `json:"status"`
	ProposalID string   `json:"proposal_id,omitempty"`
	Result     string   `json:"result,omitempty"`
	Diff       string   `json:"diff,omitempty"`
	Files      []string `json:"files,omitempty"`
	Action     string   `json:"action,omitempty"`
	Error      string   `json:"error,omitempty"`
	Next       []string `json:"next,omitempty"`
}

// proposalVerbs is the settlement vocabulary advertised to the orchestrator,
// mirroring Shepherd's run select | apply | release | discard.
func proposalVerbs() []string { return []string{"select", "apply", "release", "discard"} }

// retainedProposal is one sealed sub-agent run awaiting settlement: the isolated
// child scope that owns the worktree, and the retained output its workspace was
// sealed into (nil when the run errored before sealing).
type retainedProposal struct {
	id       string
	scopeID  string
	workdir  string
	output   *shepherd.RetainedOutput
	result   string
	runErr   string
	parentID string
	mgr      *shepherd.ScopeManager
}

// proposalSeq disambiguates proposal ids minted within the same nanosecond
// (parallel dispatches are invited), so ids — and their worktree paths — never
// collide.
var proposalSeq atomic.Uint64

// retainedProposals is the process-wide registry of open proposals. It is
// process-scoped: a proposal outlives its dispatch call and is settled later by
// the orchestrator.
var retainedProposals = struct {
	mu   sync.Mutex
	byID map[string]*retainedProposal
}{byID: make(map[string]*retainedProposal)}

// resetRetainedProposals clears the registry (tests only).
func resetRetainedProposals() {
	retainedProposals.mu.Lock()
	defer retainedProposals.mu.Unlock()
	retainedProposals.byID = make(map[string]*retainedProposal)
}

func getRetainedProposal(id string) (*retainedProposal, error) {
	retainedProposals.mu.Lock()
	p, ok := retainedProposals.byID[id]
	retainedProposals.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("proposal %q not found — it may already have been settled", id)
	}
	return p, nil
}

// startRetainedRun dispatches one sub-agent into an isolated worktree and seals
// its delta into a proposal, returning the proposal envelope. A body error still
// yields a sealed (partial) proposal, exactly as the review fork does; only a
// provisioning or sealing failure has no output.
func startRetainedRun(ctx context.Context, rt RetainedRuntime, prompt string, subParams jobs.SubAgentParams, timeout time.Duration) (string, error) {
	if rt.Runner == nil {
		return "", fmt.Errorf("spawn_subagent: sub-agent runner not configured")
	}
	mgr := rt.ScopeManager
	if mgr == nil {
		return "", fmt.Errorf("spawn_subagent: retained mode needs shepherd tracing (set shepherd_trace_dir)")
	}
	if rt.RootScopeID == "" {
		return "", fmt.Errorf("spawn_subagent: retained mode needs a shepherd root scope to fork from")
	}
	parent, ok := mgr.Get(rt.RootScopeID)
	if !ok {
		return "", fmt.Errorf("spawn_subagent: root scope %s not found", rt.RootScopeID)
	}

	// A repo path defaults to the process working directory, as root-scope
	// creation does — so a session that never set retained_repo_path still gets a
	// real worktree repository.
	if rt.RepoPath == "" {
		wd, werr := os.Getwd()
		if werr != nil {
			return "", fmt.Errorf("spawn_subagent: resolve repo path: %w", werr)
		}
		rt.RepoPath = wd
	}

	// The basis: the parent's workspace at dispatch. The proposal's delta is
	// everything the sub-agent changes relative to it.
	forkPoint, err := parent.CaptureWorkspace(ctx)
	if err != nil {
		return "", fmt.Errorf("spawn_subagent: capture basis: %w", err)
	}

	id := fmt.Sprintf("proposal:%d:%d", time.Now().UnixNano(), proposalSeq.Add(1))
	wt := retainedWorktreePath(rt, id)

	scopeID, provErr := provisionRetainedBranch(ctx, rt, id, wt, forkPoint)
	env := proposalEnvelope{ProposalID: id, Next: proposalVerbs()}
	if scopeID == "" {
		// Nothing ran and nothing was sealed: report, register nothing.
		env.Status = "error"
		env.Error = provErr.Error()
		return marshalProposalEnvelope(env), nil
	}

	sp := subParams
	sp.Workdir = wt
	result, bodyErr := runRetainedBody(ctx, rt, prompt, sp, timeout)

	// Scrub the confinement scratch before sealing: the env redirect keeps the
	// body's housekeeping writes (HOME/TMPDIR/caches) inside the worktree, and
	// the scrub keeps them out of the proposal's delta.
	ScrubConfinementScratch(wt)

	// Seal even after a body error: the partial work is reviewable. A seal
	// failure is surfaced, never dropped.
	output, sealErr := mgr.Seal(ctx, scopeID)
	var notes []string
	if provErr != nil {
		notes = append(notes, provErr.Error())
	}
	if sealErr != nil {
		notes = append(notes, "seal: "+sealErr.Error())
	}
	if len(notes) > 0 {
		env.Error = strings.Join(notes, "; ")
	}

	if output == nil {
		// Sealing failed: the isolated scope still owns its worktree, so
		// discard it rather than strand it. There is nothing to settle.
		if scope, gok := mgr.Get(scopeID); gok {
			_ = parent.Discard(scope)
		}
		env.Status = "error"
		env.Result = trimResult(result)
		return marshalProposalEnvelope(env), nil
	}

	retainedProposals.mu.Lock()
	retainedProposals.byID[id] = &retainedProposal{
		id:       id,
		scopeID:  scopeID,
		workdir:  wt,
		output:   output,
		result:   trimResult(result),
		runErr:   errorString(bodyErr),
		parentID: rt.RootScopeID,
		mgr:      mgr,
	}
	retainedProposals.mu.Unlock()

	env.Status = "proposal"
	env.Result = trimResult(result)
	env.Diff = clipDiff(output.Diff())
	env.Files = changesPaths(output.Changes())
	return marshalProposalEnvelope(env), nil
}

// settleRetainedProposal settles one proposal with the given verb, once.
func settleRetainedProposal(ctx context.Context, id, action string) (string, error) {
	p, err := getRetainedProposal(id)
	if err != nil {
		return "", err
	}
	if p.output == nil {
		return "", fmt.Errorf("proposal %s has no sealed output to settle", id)
	}
	verb, ok := settlementVerb(action)
	if !ok {
		return "", fmt.Errorf("proposal: unknown action %q (select, apply, release, discard)", action)
	}

	env := proposalEnvelope{Status: "settled", ProposalID: id, Action: action}
	if err := p.output.Settle(ctx, verb); err != nil {
		switch {
		case errors.Is(err, shepherd.ErrOutputConsumed):
			return "", fmt.Errorf("proposal %s was already settled — a sealed output is consumed once", id)
		case errors.Is(err, shepherd.ErrApplyConflict):
			return "", fmt.Errorf("proposal %s: the parent changed paths this proposal also changes (%w); resolve the overlap, then apply or discard", id, err)
		case errors.Is(err, shepherd.ErrSettleEffectLanded):
			// The work is on the parent; only the causal merge failed to finish.
			// End the proposal with the failure noted — a retry would re-apply
			// an effect that already happened.
			env.Error = err.Error()
		default:
			return "", fmt.Errorf("proposal %s: %w", id, err)
		}
	}

	// Release integrates nothing and leaves the isolated scope — and its
	// worktree — untouched (the kernel's release is "keep, don't integrate"), so
	// tear the child down here rather than strand the worktree. Select, Apply,
	// and Discard already end the child scope themselves.
	if verb == shepherd.SettleRelease {
		teardownReleasedProposal(p)
	}

	// Consume-once: a settled proposal leaves the registry.
	retainedProposals.mu.Lock()
	delete(retainedProposals.byID, id)
	retainedProposals.mu.Unlock()
	return marshalProposalEnvelope(env), nil
}

// teardownReleasedProposal discards the isolated child scope a released
// proposal owned, removing its worktree. Best-effort: the record is already
// settled either way.
func teardownReleasedProposal(p *retainedProposal) {
	if p.mgr == nil {
		return
	}
	parent, ok := p.mgr.Get(p.parentID)
	if !ok {
		return
	}
	scope, ok := p.mgr.Get(p.scopeID)
	if !ok {
		return
	}
	_ = parent.Discard(scope)
}

// settlementVerb maps a proposal verb to the kernel's settlement action.
func settlementVerb(action string) (shepherd.SettlementAction, bool) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "select":
		return shepherd.SettleSelect, true
	case "apply":
		return shepherd.SettleApply, true
	case "release":
		return shepherd.SettleRelease, true
	case "discard":
		return shepherd.SettleDiscard, true
	default:
		return "", false
	}
}

// listRetainedProposalsSummaries renders the open proposals.
func listRetainedProposalsSummaries() string {
	retainedProposals.mu.Lock()
	ps := make([]*retainedProposal, 0, len(retainedProposals.byID))
	for _, p := range retainedProposals.byID {
		ps = append(ps, p)
	}
	retainedProposals.mu.Unlock()

	if len(ps) == 0 {
		return "No open proposals."
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].id < ps[j].id })

	var b strings.Builder
	b.WriteString("Open proposals:\n")
	for _, p := range ps {
		var files []string
		if p.output != nil {
			files = changesPaths(p.output.Changes())
		}
		fmt.Fprintf(&b, "  %s  files=%d", p.id, len(files))
		if p.runErr != "" {
			fmt.Fprintf(&b, "  (run error: %s)", p.runErr)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// provisionRetainedBranch creates the worktree, forks an isolated child scope
// over it, seeds it with the fork point, and bootstraps it. On any failure it
// returns "" and the error; the caller has nothing to settle and the worktree
// (if created) has been torn down.
func provisionRetainedBranch(
	ctx context.Context,
	rt RetainedRuntime,
	ownerID, wtPath string,
	forkPoint shepherdtrace.WorkspaceState,
) (string, error) {
	sb := shepherdgit.NewWorktreeSandbox(rt.RepoPath, wtPath)
	if err := sb.Create(ctx, shepherdsandbox.SandboxSpec{}); err != nil {
		// A worktree left behind by a killed run can occupy the path. Only
		// clear it when it is actually a worktree registered with this repo.
		if recovery.IsRegisteredWorktree(ctx, rt.RepoPath, wtPath) {
			_ = sb.Destroy(context.WithoutCancel(ctx))
		}
		if retryErr := sb.Create(ctx, shepherdsandbox.SandboxSpec{}); retryErr != nil {
			return "", fmt.Errorf("create worktree %s: %v (first attempt: %v)", wtPath, retryErr, err)
		}
	}

	mgr := rt.ScopeManager
	scope, err := mgr.ForkIsolated(rt.RootScopeID, ownerID, nil, sb)
	if err != nil {
		_ = sb.Destroy(context.WithoutCancel(ctx))
		return "", fmt.Errorf("fork retained scope: %w", err)
	}

	// Seed the checkout with the parent's exact state at the fork point: a
	// worktree checks out tracked files only, and Apply restores the parent's
	// uncommitted changes and deletions so the sealed delta is exact.
	if err := sb.Apply(ctx, forkPoint); err != nil {
		return scope.ID(), fmt.Errorf("seed basis: %w", err)
	}
	if rt.WorktreeBootstrap != "" {
		if err := runWorktreeBootstrap(ctx, wtPath, rt.WorktreeBootstrap); err != nil {
			return scope.ID(), fmt.Errorf("bootstrap: %w", err)
		}
	}
	return scope.ID(), nil
}

// runRetainedBody invokes the sub-agent runner with its workdir set to the
// worktree.
func runRetainedBody(ctx context.Context, rt RetainedRuntime, prompt string, sp jobs.SubAgentParams, timeout time.Duration) (string, error) {
	runCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return rt.Runner(runCtx, prompt, sp)
}

// retainedWorktreePath returns the worktree directory for a proposal.
func retainedWorktreePath(rt RetainedRuntime, id string) string {
	root := rt.WorktreeRoot
	if root == "" {
		root = filepath.Join(filepath.Dir(rt.RepoPath), "shepherd-worktrees")
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	safe := strings.NewReplacer(":", "-", "/", "-", "\\", "-").Replace(id)
	return filepath.Join(root, safe)
}

func marshalProposalEnvelope(env proposalEnvelope) string {
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Sprintf(`{"status":%q}`, env.Status)
	}
	return string(data)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// clipDiff bounds a sealed output's diff to the envelope's line budget.
// The kernel seals diffs unbounded; an envelope must not bloat the
// orchestrator's context.
func clipDiff(diff string) string {
	lines := strings.Split(diff, "\n")
	if len(lines) <= retainedDiffMaxLines {
		return diff
	}
	return strings.Join(lines[:retainedDiffMaxLines], "\n") + "\n...[truncated]"
}

// retainedDiffMaxLines bounds a proposal envelope's diff.
const retainedDiffMaxLines = 200

// changesPaths projects a sealed output's frozen changes as the changed file
// list the envelopes carry.
func changesPaths(changes []shepherd.ProposedChange) []string {
	if len(changes) == 0 {
		return nil
	}
	paths := make([]string, 0, len(changes))
	for _, c := range changes {
		paths = append(paths, c.Path)
	}
	return paths
}

// trimResult normalizes a sub-agent report for envelopes: trims whitespace and
// caps length.
func trimResult(s string) string {
	const maxResult = 4000
	s = strings.TrimRight(s, " \t\r\n")
	if len(s) > maxResult {
		return s[:maxResult] + "\n...[truncated]"
	}
	return s
}

// runWorktreeBootstrap runs the configured bootstrap command inside a freshly
// created worktree. A git worktree checks out tracked files only, so a repo
// whose build needs gitignored artifacts (node_modules, caches, .env) recreates
// them here.
func runWorktreeBootstrap(ctx context.Context, dir, command string) error {
	shell, shellArg := "sh", "-c"
	if runtime.GOOS == "windows" {
		shell, shellArg = "pwsh", "-Command"
		if _, err := exec.LookPath("pwsh"); err != nil {
			shell, shellArg = "powershell", "-Command"
		}
	}
	cmd := exec.CommandContext(ctx, shell, shellArg, command)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
