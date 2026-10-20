package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	// Runner is the sub-agent execution closure (the same one delegate
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
	// defaults to ".yaah/shepherd-worktrees" inside the repository working
	// tree (project-local).
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
// startRetainedRun runs one retained dispatch. When report is set
// (yaah-7yf), the isolation is kept but the sealing is skipped: the
// result is the sub-agent's report text, the worktree is torn down
// before returning, and no proposal is registered — a report-role
// dispatch carries no delta to settle.
func startRetainedRun(ctx context.Context, rt RetainedRuntime, prompt string, subParams jobs.SubAgentParams, timeout time.Duration, report bool) (string, error) {
	if rt.Runner == nil {
		return "", fmt.Errorf("delegate: sub-agent runner not configured")
	}
	mgr := rt.ScopeManager
	if mgr == nil {
		return "", fmt.Errorf("delegate: retained mode needs the shepherd scope manager (trace store unavailable)")
	}
	if rt.RootScopeID == "" {
		return "", fmt.Errorf("delegate: retained mode needs a shepherd root scope to fork from")
	}
	parent, ok := mgr.Get(rt.RootScopeID)
	if !ok {
		return "", fmt.Errorf("delegate: root scope %s not found", rt.RootScopeID)
	}

	// A repo path defaults to the process working directory, as root-scope
	// creation does — so a session that never set retained_repo_path still gets a
	// real worktree repository.
	if rt.RepoPath == "" {
		wd, werr := os.Getwd()
		if werr != nil {
			return "", fmt.Errorf("delegate: resolve repo path: %w", werr)
		}
		rt.RepoPath = wd
	}

	// The basis: the parent's workspace at dispatch. The proposal's delta is
	// everything the sub-agent changes relative to it.
	forkPoint, err := parent.CaptureWorkspace(ctx)
	if err != nil {
		return "", fmt.Errorf("delegate: capture basis: %w", err)
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

	// The dispatch budget is a wall-clock limit on the whole run. Track which
	// deadline governed the run context so a parent deadline (or a provider's
	// own per-request timeout) is not misreported as role-budget exhaustion.
	budgetDeadline := time.Now().Add(timeout)
	parentDeadline, parentHasDeadline := ctx.Deadline()
	budgetGoverns := timeout > 0 && (!parentHasDeadline || parentDeadline.After(budgetDeadline))

	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	result, bodyErr := runRetainedBody(runCtx, rt, prompt, sp)
	runErr := runCtx.Err()
	parentErr := ctx.Err()
	if cancel != nil {
		cancel()
	}

	// The report lane (yaah-7yf) never seals: the deliverable is the
	// result text. Surface any provisioning problem the proposal lane
	// would have put in the envelope (seed/bootstrap failures return a
	// scope AND an error), tear the worktree down — the scratch goes with
	// it — and hand the report straight back.
	if report {
		if provErr != nil {
			result = strings.TrimSpace(result + "\n\nnote: " + provErr.Error())
		}
		if scope, gok := mgr.Get(scopeID); gok {
			if derr := parent.Discard(scope); derr != nil {
				// A report dispatch registers nothing, so a leaked
				// worktree has no proposal record to clean up through;
				// at least say so.
				slog.Warn("retained report: worktree discard failed", "worktree", wt, "err", derr)
			}
		} else if recovery.IsRegisteredWorktree(ctx, rt.RepoPath, wt) {
			if derr := shepherdgit.NewWorktreeSandbox(rt.RepoPath, wt).Destroy(context.WithoutCancel(ctx)); derr != nil {
				slog.Warn("retained report: worktree destroy failed", "worktree", wt, "err", derr)
			}
		} else {
			slog.Warn("retained report: scope missing and worktree unregistered; nothing to tear down", "scope", scopeID, "worktree", wt)
		}
		return result, bodyErr
	}

	// Scrub the confinement scratch before sealing: the env redirect keeps the
	// body's housekeeping writes (HOME/TMPDIR/caches) inside the worktree, and
	// the scrub keeps them out of the proposal's delta.
	ScrubConfinementScratch(wt)

	// Seal even after a body error: the partial work is reviewable. A seal
	// failure is surfaced, never dropped.
	output, sealErr := mgr.Seal(ctx, scopeID)
	var notes []string
	bodyFailure := ""
	if bodyErr != nil {
		// The run stopped early (timeout, halt, provider error) but its partial
		// work was still sealed. Say why in the envelope so the orchestrator
		// does not read a truncated result as a finished one.
		source := classifyRetainedBodyFailure(bodyErr, timeout, runErr, parentErr, budgetGoverns)
		bodyFailure = retainedBodyFailure(bodyErr, sp.Role, timeout, source)
		notes = append(notes, bodyFailure)
	}
	if provErr != nil {
		notes = append(notes, provErr.Error())
	}
	if sealErr != nil {
		notes = append(notes, "seal: "+sealErr.Error())
	}
	if len(notes) > 0 {
		// Same bound as the result: a provider error can carry a trace profile
		// or a long provider message, and the envelope must stay reviewable.
		env.Error = trimResult(strings.Join(notes, "; "))
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
		runErr:   bodyFailure,
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
	// Hide the worktree root from the target repo's git status before the
	// worktree exists. Best-effort: failure only means the repo may surface
	// the scratch dirs as untracked; the worktree itself is unaffected.
	_ = ensureWorktreeRootExcluded(ctx, rt)

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
// worktree. The caller owns any dispatch timeout on ctx.
func runRetainedBody(ctx context.Context, rt RetainedRuntime, prompt string, sp jobs.SubAgentParams) (string, error) {
	return rt.Runner(ctx, prompt, sp)
}

// retainedWorktreeRoot returns the absolute parent directory for retained
// worktrees.
func retainedWorktreeRoot(rt RetainedRuntime) string {
	root := rt.WorktreeRoot
	if root == "" {
		root = filepath.Join(rt.RepoPath, ".yaah", "shepherd-worktrees")
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return root
}

// retainedWorktreePath returns the worktree directory for a proposal.
func retainedWorktreePath(rt RetainedRuntime, id string) string {
	safe := strings.NewReplacer(":", "-", "/", "-", "\\", "-").Replace(id)
	return filepath.Join(retainedWorktreeRoot(rt), safe)
}

// ensureWorktreeRootExcluded makes the retained-worktree root invisible to the
// target repo's git status. The .yaah/ entry in yaah's own .gitignore only
// protects yaah's checkout; when retained_repo_path points at a different repo,
// that repo would otherwise see the worktrees as untracked content (and could
// sweep them into a commit, or gitsandbox could evacuate them). We append the
// root — relative to that repo — to its .git/info/exclude, a local, uncommitted
// exclusion.
func ensureWorktreeRootExcluded(ctx context.Context, rt RetainedRuntime) error {
	repoPath, err := filepath.Abs(rt.RepoPath)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(repoPath, retainedWorktreeRoot(rt))
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil // root is outside the repo; nothing to exclude
	}
	return appendGitInfoExclude(ctx, repoPath, filepath.ToSlash(rel))
}

// appendGitInfoExclude appends a relative path (gitignore syntax, directory
// semantics) to the target repo's .git/info/exclude, unless already present.
func appendGitInfoExclude(ctx context.Context, repoPath, rel string) error {
	excludePath, err := gitInfoExcludePath(ctx, repoPath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	line := strings.TrimSuffix(rel, "/") + "/"
	for _, existing := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(existing) == line {
			return nil // already present
		}
	}

	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(f, "%s\n", line)
	return err
}

// gitInfoExcludePath resolves the absolute path of the target repo's
// .git/info/exclude, handling linked worktrees (where .git is a file).
func gitInfoExcludePath(ctx context.Context, repoPath string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
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

type retainedFailureSource int

const (
	// retainedFailureProvider is a provider or body error while the dispatch
	// context was still live (for example a provider's own request timeout).
	retainedFailureProvider retainedFailureSource = iota
	// retainedFailureBudget is the role/global wall-clock budget expiring.
	retainedFailureBudget
	// retainedFailureParent is the parent context deadline expiring first.
	retainedFailureParent
)

// classifyRetainedBodyFailure identifies which deadline actually stopped the
// run. A body error that is a deadline while both contexts remain live is a
// provider-internal timeout, not budget exhaustion.
func classifyRetainedBodyFailure(err error, timeout time.Duration, runErr, parentErr error, budgetGoverns bool) retainedFailureSource {
	if !errors.Is(err, context.DeadlineExceeded) {
		return retainedFailureProvider
	}
	if budgetGoverns && runErr == context.DeadlineExceeded {
		return retainedFailureBudget
	}
	if parentErr == context.DeadlineExceeded {
		return retainedFailureParent
	}
	if timeout > 0 && runErr == context.DeadlineExceeded {
		// The run context expired, but the parent deadline was earlier.
		return retainedFailureParent
	}
	return retainedFailureProvider
}

// retainedBodyFailure describes why a retained run stopped. Budget and parent
// deadlines must not be reported as provider rejections: name the role and the
// budget to raise, or the parent deadline that actually fired, so the
// orchestrator does not chase the service that was merely in flight.
func retainedBodyFailure(err error, role string, timeout time.Duration, source retainedFailureSource) string {
	if !errors.Is(err, context.DeadlineExceeded) {
		return errorString(err)
	}
	switch source {
	case retainedFailureBudget:
		if timeout <= 0 {
			break
		}
		msg := "sub-agent timed out after " + timeout.String()
		if role != "" {
			msg += " (role " + role + ")"
		}
		return msg + "; raise the role timeout or agents.subagent.default_timeout for long reasoning runs"
	case retainedFailureParent:
		msg := "sub-agent run ended because the parent context deadline expired"
		if role != "" {
			msg += " (role " + role + ")"
		}
		if timeout > 0 {
			msg += " before its " + timeout.String() + " dispatch budget"
		}
		return msg
	}
	// Provider-internal deadline while the dispatch was live (or an
	// unclassified deadline): preserve the original error.
	return errorString(err)
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

// retainedDiffMaxLines bounds a proposal envelope's diff. It matches the review
// envelope's old budget: an orchestrator settling a proposal needs the change,
// and a diff it cannot read is a proposal it cannot judge.
const retainedDiffMaxLines = 2000

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
