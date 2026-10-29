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

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherd "github.com/buchenberg/goatherd-runtime"
	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
	goatherdgit "github.com/buchenberg/goatherd-runtime/sandbox/git"
	"github.com/buchenberg/yaah/internal/agent/recovery"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/jail"
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
	ScopeManager *goatherd.ScopeManager
	// RootScopeID is the session's root scope — the fork parent, and the merge
	// target a settlement applies to. Required.
	RootScopeID string
	// SessionID is the durable session identity. Container proposals persist
	// it so a resumed session can rehydrate its own artifacts without another
	// session in the same repository claiming them.
	SessionID string
	// WorktreeRoot is the parent directory for retained worktrees. Empty
	// defaults to ".yaah/shepherd-worktrees" inside the repository working
	// tree (project-local).
	WorktreeRoot string
	// WorktreeBootstrap runs inside each worktree after creation, for repos
	// whose build needs gitignored artifacts.
	WorktreeBootstrap string
	// Proposals is the session's registry of open proposals: the dispatch
	// records a sealed proposal here and the proposal tool settles it from
	// here. Required when the retained lane is live: runner.NewDelegateTool
	// creates one per session at construction (yaah-yox), so isolation is a
	// property of building the runtime rather than of a later wiring step.
	Proposals *ProposalRegistry

	// World is the configured dispatch world (agents.subagent.world:
	// auto | worktree | container). Empty means auto. WorldForRole supplies a
	// per-role override; nil means no override.
	World        string
	WorldForRole func(role string) string

	// Containment and Network are the configured authority axes for a
	// confined dispatch. They participate in world resolution because a
	// fallback is legal only when it can still enforce them.
	Containment string
	Network     string

	// Grants is the configured dispatch grant set; GrantsForRole supplies a
	// per-role override. They participate in world resolution: a container
	// world cannot lower grants yet, so a restrictive set refuses one.
	Grants        []config.GrantConfig
	GrantsForRole func(role string) []config.GrantConfig

	// ContainerWorlds reports whether this session can attempt a per-dispatch
	// container world (yaah-ihfh.3). It is true only in a host session whose
	// config names a container image; a session that offers no container world
	// degrades silently to the worktree, while an explicitly requested
	// container fails closed.
	ContainerWorlds bool

	// DispatchWorlds is the setup a per-dispatch container needs: the same
	// workspace.containerd stanza the session container uses, the backend
	// opener, and the resolved copy-in deny list. Nil when this session cannot
	// offer one, in which case a report dispatch runs in the worktree world.
	DispatchWorlds *DispatchWorlds
}

// DispatchWorlds carries what a per-dispatch container world needs. The
// composition root builds it from the session's workspace config, so the
// dispatch container and the session container share one image contract and one
// set of connection settings rather than drifting apart.
type DispatchWorlds struct {
	// Config is the workspace stanza (image, namespace, address, snapshotter,
	// fifo_dir, root).
	Config config.WorkspaceConfig
	// Opener constructs the sandbox. Wiring passes
	// OpenContainerdWorkspaceSandbox; tests pass a fake, so no test needs a
	// containerd daemon.
	Opener SandboxOpener
	// Deny is agents.default.workspace_deny_patterns with its defaults
	// already resolved. It is passed through rather than recomputed: the two
	// substrates must copy the same files in, or a secret readable in one
	// world would be absent from the other.
	Deny []string
	// SessionSandbox is the container session's own sandbox when this dispatch
	// setup belongs to a container session. Its current state seeds each
	// sibling container; a nil value means the host tree is the source (the
	// local-session lane).
	SessionSandbox *SessionSandbox
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
	// Retained is the path of the exported delta for a release, proving the
	// work survived the teardown that follows it.
	Retained string `json:"retained,omitempty"`
	// Notes carries caveats that do not invalidate the settlement — e.g. a
	// failed teardown that stranded the worktree.
	Notes string `json:"notes,omitempty"`
	// Outcome is the rendered Outcome{Work,Scope,Record,Notes} block for the
	// settlement (yaah-lka3: report state, not just a completion word).
	Outcome string `json:"outcome,omitempty"`
	// World is the resolved dispatch world ("worktree" or "container"). It is
	// absent on a shared-lane dispatch, which has no world.
	World string `json:"world,omitempty"`
	// Warning carries a world degradation: the dispatch ran, but not in the
	// world that was asked for. The envelope shape and the settlement verbs do
	// not change; only this line does.
	Warning string   `json:"warning,omitempty"`
	Next    []string `json:"next,omitempty"`
}

// proposalVerbs is the settlement vocabulary advertised to the orchestrator,
// mirroring Shepherd's run select | apply | release | discard.
func proposalVerbs() []string { return []string{"select", "apply", "release", "discard"} }

// retainedProposal is one sealed sub-agent run awaiting settlement: the isolated
// child scope that owns the worktree, and the retained output its workspace was
// sealed into (nil when the run errored before sealing).
//
// A container proposal carries no live scope and no sealed kernel output: the
// container is destroyed at seal and the proposal is the GitDelta manifest.
// Exactly one of output (worktree lane) and delta (container lane) is set for a
// sealed proposal.
type retainedProposal struct {
	id       string
	scopeID  string
	workdir  string
	output   *goatherd.RetainedOutput
	delta    *GitDelta
	review   string
	result   string
	runErr   string
	parentID string
	mgr      *goatherd.ScopeManager

	// Container-lane settlement state. repoPath is the host repository the
	// manifest settles onto; basisDigest is the host workspace state captured
	// before the copy-in, which select compares against; artifactDir is the
	// durable proposal directory; world records the lane for envelopes.
	repoPath    string
	basisState  shepherdtrace.WorkspaceState
	basisDigest string
	artifactDir string
	deltaDigest string
	world       string
}

// changedFiles is the proposal's file list, whichever lane produced it.
func (p *retainedProposal) changedFiles() []string {
	switch {
	case p.delta != nil:
		return p.delta.Paths()
	case p.output != nil:
		return changesPaths(p.output.Changes())
	default:
		return nil
	}
}

// reviewDiff is the proposal's reviewable diff, whichever lane produced it.
func (p *retainedProposal) reviewDiff() string {
	switch {
	case p.review != "":
		return p.review
	case p.delta != nil:
		return p.delta.RenderDiff()
	case p.output != nil:
		return p.output.Diff()
	default:
		return ""
	}
}

// proposalSeq disambiguates proposal ids minted within the same nanosecond
// (parallel dispatches are invited), so ids — and their worktree paths — never
// collide.
var proposalSeq atomic.Uint64

// ProposalRegistry is one session's registry of open proposals. A proposal
// outlives the dispatch call that sealed it — the orchestrator settles it later
// — but it never leaves the session that owns it: under `web` or `acp-serve`,
// one session's tool must not list, show (which prints the diff), or settle
// another session's work (yaah-yox).
type ProposalRegistry struct {
	mu   sync.Mutex
	byID map[string]*retainedProposal
}

// NewProposalRegistry returns an empty registry for one session.
func NewProposalRegistry() *ProposalRegistry {
	return &ProposalRegistry{byID: make(map[string]*retainedProposal)}
}

// add records a sealed proposal as open.
func (r *ProposalRegistry) add(p *retainedProposal) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byID == nil {
		r.byID = make(map[string]*retainedProposal)
	}
	r.byID[p.id] = p
}

// get returns an open proposal, or an error naming the id it could not find.
func (r *ProposalRegistry) get(id string) (*retainedProposal, error) {
	r.mu.Lock()
	p, ok := r.byID[id]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("proposal %q not found — it may already have been settled", id)
	}
	return p, nil
}

// remove consumes a settled proposal: settlement is exactly once.
func (r *ProposalRegistry) remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byID, id)
}

// all returns the open proposals, ordered by id so a listing is stable.
func (r *ProposalRegistry) all() []*retainedProposal {
	r.mu.Lock()
	ps := make([]*retainedProposal, 0, len(r.byID))
	for _, p := range r.byID {
		ps = append(ps, p)
	}
	r.mu.Unlock()
	sort.Slice(ps, func(i, j int) bool { return ps[i].id < ps[j].id })
	return ps
}

// proposalSummary is one entry of the list envelope: the id to settle and the
// files the proposal changes.
type proposalSummary struct {
	ProposalID string   `json:"proposal_id"`
	Files      []string `json:"files"`
}

// summaries renders the open proposals as prose, for a human reader.
func (r *ProposalRegistry) summaries() string {
	ps := r.all()
	if len(ps) == 0 {
		return "No open proposals."
	}

	var b strings.Builder
	b.WriteString("Open proposals:\n")
	for _, p := range ps {
		files := p.changedFiles()
		fmt.Fprintf(&b, "  %s  files=%d", p.id, len(files))
		if p.world != "" {
			fmt.Fprintf(&b, "  world=%s", p.world)
		}
		if p.runErr != "" {
			fmt.Fprintf(&b, "  (run error: %s)", p.runErr)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// listEnvelope is the JSON a `list` action answers with: the same envelope
// shape the other verbs use, so a caller parses one format. An empty registry
// still carries "proposals": [], because "nothing is open" must be a parsed
// fact rather than prose a caller cannot tell from a failed parse (yaah-yox).
func (r *ProposalRegistry) listEnvelope() string {
	ps := r.all()
	list := make([]proposalSummary, 0, len(ps))
	for _, p := range ps {
		files := p.changedFiles()
		if files == nil {
			files = []string{}
		}
		list = append(list, proposalSummary{ProposalID: p.id, Files: files})
	}
	env := struct {
		Status    string            `json:"status"`
		Proposals []proposalSummary `json:"proposals"`
		Next      []string          `json:"next,omitempty"`
	}{Status: "open", Proposals: list, Next: proposalVerbs()}
	data, err := json.Marshal(env)
	if err != nil {
		return `{"status":"open","proposals":[]}`
	}
	return string(data)
}

// sessionContainerFallback runs a report dispatch in the container session
// itself when a sibling world is unavailable or policy-refused. It records the
// degradation on the root owner path and carries it into the report, so the
// trace and the orchestrator both see the weaker world.
func sessionContainerFallback(
	ctx context.Context,
	rt RetainedRuntime,
	prompt string,
	subParams jobs.SubAgentParams,
	timeout time.Duration,
	requested subagent.WorldKind,
	reason string,
) (string, error) {
	if requested == "" {
		requested = subagent.WorldAuto
	}
	recordWorldFact(ctx, rt.ScopeManager, rt.RootScopeID, subagent.WorldResolution{
		Requested: requested,
		Resolved:  subagent.WorldShared,
		Degraded:  true,
		Reason:    reason,
	})
	// The body runs in the session container, not in the worktree the
	// step-down may have named, so the dispatch event carries shared.
	recordDispatchWorld(ctx, subagent.WorldResolution{
		Requested: requested,
		Resolved:  subagent.WorldShared,
		Degraded:  true,
		Reason:    reason,
	})
	runCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	result, err := rt.Runner(runCtx, prompt, subParams)
	if err != nil {
		return "", err
	}
	return retainedNote(result, "dispatch world degraded: "+reason), nil
}

// startRetainedRun dispatches one sub-agent into an isolated worktree and seals
// its delta into a proposal, returning the proposal envelope. A body error still
// yields a sealed (partial) proposal, exactly as the review fork does; only a
// provisioning or sealing failure has no output.
//
// When report is set (yaah-7yf), the isolation is kept but the sealing is
// skipped: the result is the sub-agent's report text, the world is torn down
// before returning, and no proposal is registered — a report-role dispatch
// carries no delta to settle.
func startRetainedRun(ctx context.Context, rt RetainedRuntime, prompt string, subParams jobs.SubAgentParams, timeout time.Duration, report bool) (string, error) {
	if rt.Runner == nil {
		return "", fmt.Errorf("delegate: sub-agent runner not configured")
	}
	mgr := rt.ScopeManager
	if mgr == nil {
		return "", fmt.Errorf("delegate: retained mode needs the goatherd scope manager (trace store unavailable)")
	}
	if rt.RootScopeID == "" {
		return "", fmt.Errorf("delegate: retained mode needs a goatherd root scope to fork from")
	}
	// The registry is required, not defaulted: a process-wide fallback would
	// let one session list, show, or settle another's proposal. The runtime is
	// built by runner.NewDelegateTool, which mints one per session (yaah-yox).
	if rt.Proposals == nil {
		return "", fmt.Errorf("delegate: retained mode needs a proposal registry — runner.NewDelegateTool creates one per session")
	}
	if rt.DispatchWorlds != nil && rt.DispatchWorlds.SessionSandbox != nil && !report {
		return "", fmt.Errorf("delegate: container-session proposals stay unretained on the session container; route the role shared")
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

	// Resolve the dispatch world before anything is provisioned. A refusal
	// here fails the dispatch; a degradation is recorded and carried into the
	// envelope. The world is decided once, before the body starts: switching
	// worlds after work exists would change the basis of the sealed output.
	requested := strings.TrimSpace(rt.World)
	if rt.WorldForRole != nil {
		if w := strings.TrimSpace(rt.WorldForRole(subParams.Role)); w != "" {
			requested = w
		}
	}
	explicitWorld := strings.EqualFold(requested, "container") || strings.EqualFold(requested, "worktree")
	requestedKind := subagent.WorldKind(strings.ToLower(strings.TrimSpace(requested)))
	if requestedKind == "" {
		requestedKind = subagent.WorldAuto
	}
	containerSession := rt.DispatchWorlds != nil && rt.DispatchWorlds.SessionSandbox != nil

	worldRes, werr := resolveDispatchWorld(rt, subParams.Role, report)
	if werr != nil {
		// Only an unavailable world is a ladder fact we may degrade from. A
		// typo'd world or an explicit worktree/container request is a
		// configuration error and must surface.
		if containerSession && report && !explicitWorld && errors.Is(werr, subagent.ErrWorldUnavailable) {
			// The session container is the predecessor world in the ladder;
			// a report seals nothing, so it can run there unretained.
			return sessionContainerFallback(ctx, rt, prompt, subParams, timeout, requestedKind, werr.Error())
		}
		return "", fmt.Errorf("delegate: %w", werr)
	}
	recordDispatchWorld(ctx, worldRes)

	// A container world serves both lanes. A report never seals, so the
	// container is simply destroyed. A proposal seals a GitDelta manifest
	// before the container is destroyed: the manifest is the proposal, so no
	// live world (and no kernel output) has to survive settlement.
	if worldRes.Resolved == subagent.WorldContainer {
		var basis shepherdtrace.WorkspaceState
		var basisDigest string
		if !report {
			// The select precondition is the host workspace state captured
			// before the copy-in. Proposal bookkeeping is excluded from git
			// first: the manifest lives in the host tree, and writing it after
			// the basis capture would otherwise move the very state select
			// compares against.
			if err := ensureProposalPathsExcluded(ctx, rt.RepoPath); err != nil {
				return "", fmt.Errorf("delegate: container proposal: exclude proposal path: %w", err)
			}
			var cerr error
			basis, cerr = parent.CaptureWorkspace(ctx)
			if cerr != nil {
				return "", fmt.Errorf("delegate: container proposal: capture host basis: %w", cerr)
			}
			digest, derr := basis.Digest()
			if derr != nil {
				return "", fmt.Errorf("delegate: container proposal: digest host basis: %w", derr)
			}
			basisDigest = digest
		}
		ss, perr := openDispatchWorld(ctx, rt)
		if perr == nil {
			if report {
				return runDispatchContainer(ctx, rt, parent, prompt, subParams, timeout, worldRes, ss)
			}
			return runDispatchContainerProposal(ctx, rt, parent, prompt, subParams, timeout, worldRes, ss, basis, basisDigest)
		}
		// Preflight failed before any work exists: auto may step down to the
		// worktree (recording the degradation); an explicit container fails
		// closed. Nothing was provisioned, so the world can still change.
		worldRes, werr = dispatchWorldPreflightFailure(worldRes, perr)
		if werr != nil {
			return "", werr
		}
		if containerSession {
			if !report {
				return "", fmt.Errorf("delegate: sibling container unavailable in a container session: %w", perr)
			}
			return sessionContainerFallback(ctx, rt, prompt, subParams, timeout, worldRes.Requested, worldRes.Reason)
		}
		// The world changed (a preflight failure stepped auto down): the
		// event must carry the world the dispatch actually runs in.
		recordDispatchWorld(ctx, worldRes)
	}

	// The basis: the parent's workspace at dispatch. The proposal's delta is
	// everything the sub-agent changes relative to it. A container report
	// dispatch never needs it — it returns above, before the copy-in.
	forkPoint, err := parent.CaptureWorkspace(ctx)
	if err != nil {
		return "", fmt.Errorf("delegate: capture basis: %w", err)
	}

	id := fmt.Sprintf("proposal:%d:%d", time.Now().UnixNano(), proposalSeq.Add(1))
	wt := retainedWorktreePath(rt, id)

	scopeID, provErr := provisionRetainedBranch(ctx, rt, id, wt, forkPoint)
	env := proposalEnvelope{ProposalID: id, Next: proposalVerbs(), World: string(worldRes.Resolved)}
	if worldRes.Degraded {
		env.Warning = "dispatch world degraded: " + worldRes.Reason
	}
	if scopeID == "" {
		// Nothing ran and nothing was sealed: report, register nothing.
		env.Status = "error"
		env.Error = provErr.Error()
		return marshalProposalEnvelope(env), nil
	}
	recordWorldFact(ctx, mgr, scopeID, worldRes)

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
		var notes []string
		if worldRes.Degraded {
			notes = append(notes, "dispatch world degraded: "+worldRes.Reason)
		}
		if provErr != nil {
			// A provisioning error's reason is at its front, so it is
			// head-capped here — the note budget of foldRetainedPartial
			// tail-keeps, which would keep the wrong half. The report
			// body itself is bounded by the role's output_limit; only the
			// caveat gets this tool-side budget.
			notes = append(notes, truncateAt(provErr.Error(), noteBudget))
		}
		// yaah-atn: a report dispatch registers no proposal, so a scratch
		// that does not come down has no record to be recovered through
		// later. Report it in the result — the same channel provErr uses —
		// instead of a log line the orchestrator never sees.
		if note := teardownReportScratch(ctx, rt, wt, func() (bool, error) {
			scope, gok := mgr.Get(scopeID)
			if !gok {
				return false, nil
			}
			return true, parent.Discard(scope)
		}); note != "" {
			notes = append(notes, note)
		}
		caveat := strings.Join(notes, "\n\n")
		if bodyErr != nil {
			// Carry the caveat out of band: the body is model output that may
			// legitimately contain "note: " lines, so re-parsing the result to
			// find the caveat would misattribute one of them. The caller folds
			// both halves back together with the right bounds.
			return result, &retainedRunError{cause: bodyErr, note: caveat}
		}
		return retainedNote(result, caveat), nil
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

	rt.Proposals.add(&retainedProposal{
		id:       id,
		scopeID:  scopeID,
		workdir:  wt,
		output:   output,
		result:   trimResult(result),
		runErr:   bodyFailure,
		parentID: rt.RootScopeID,
		mgr:      mgr,
	})

	env.Status = "proposal"
	env.Result = trimResult(result)
	env.Diff = clipDiff(output.Diff())
	env.Files = changesPaths(output.Changes())
	return marshalProposalEnvelope(env), nil
}

// resolveDispatchWorld applies the dispatch-world ladder
// (docs/shepherd-parity.md) to one dispatch. It is pure over the runtime's
// config and this host's capabilities: it provisions nothing, so a refusal
// costs a failed call rather than a stranded world.
func resolveDispatchWorld(rt RetainedRuntime, role string, report bool) (subagent.WorldResolution, error) {
	requested := strings.TrimSpace(rt.World)
	if rt.WorldForRole != nil {
		if w := strings.TrimSpace(rt.WorldForRole(role)); w != "" {
			requested = w
		}
	}

	lane := "proposal"
	if report {
		lane = "report"
	}

	// Container proposals in a host session are durable: the manifest, host
	// basis, and settlement transition markers live under .yaah/proposals,
	// and LoadProposalRegistry rebuilds them on the next session. A container
	// session gets sibling worlds for REPORT lanes only; its proposals stay
	// on the session container, unretained, and are routed shared before this
	// resolver runs.
	containerSession := rt.DispatchWorlds != nil && rt.DispatchWorlds.SessionSandbox != nil
	containerRetains := rt.DispatchWorlds != nil && !containerSession
	containerOffered := rt.ContainerWorlds
	retainsReason := ""
	if containerSession {
		retainsReason = "container-session proposals remain unretained; report lanes use sibling containers"
	} else if !containerRetains {
		retainsReason = "container settlement is unavailable in this session: no per-dispatch container setup is wired"
	}
	worktree := !containerSession
	worktreeReason := ""
	if containerSession {
		worktreeReason = "a container session has no host worktree; report lanes use a sibling container"
	}

	av := subagent.WorldAvailability{
		// A per-dispatch container is created only where the session wired the
		// setup: a Linux host with workspace.containerd.image on session or
		// dispatch config.
		Container:              rt.ContainerWorlds,
		ContainerOffered:       containerOffered,
		ContainerRetains:       containerRetains,
		ContainerRetainsReason: retainsReason,
		ContainerReason:        "per-dispatch container worlds need workspace.containerd.image on a Linux host, and a wired dispatch setup",
		Worktree:               worktree,
		WorktreeReason:         worktreeReason,
		Jail:                   jailAvailable(),
	}

	grants := rt.Grants
	if rt.GrantsForRole != nil {
		if g := rt.GrantsForRole(role); len(g) > 0 {
			grants = g
		}
	}
	return subagent.ResolveWorld(subagent.WorldRequest{
		Requested:         requested,
		Lane:              lane,
		Retained:          !report,
		Containment:       rt.Containment,
		Network:           rt.Network,
		RestrictiveGrants: len(grants) > 0,
	}, av)
}

// jailAvailable reports whether this host can actually establish a syscall
// jail. Availability, not presence: a compiled-in-but-disabled LSM degrades.
func jailAvailable() bool {
	backend := jail.Backend()
	if backend == nil {
		return false
	}
	ok, _ := backend.Available()
	return ok
}

// recordDispatchWorld publishes the resolved world to the run context so the
// start/end notifier reports the same world the dispatch actually got. The
// write happens before the child body starts and before the runner fires the
// start notifier, so a live view never sees an unresolved dispatch.
func recordDispatchWorld(ctx context.Context, res subagent.WorldResolution) {
	WriteSubAgentWorld(ctx, SubAgentWorld{
		Resolved: string(res.Resolved),
		Degraded: res.Degraded,
		Reason:   res.Reason,
	})
}

// recordWorldFact appends the resolved world to the dispatch's scope, so a
// degraded dispatch is legible in the trace after the fact — the same channel
// confinement uses. A failure to record is advisory: the run already happened.
func recordWorldFact(ctx context.Context, mgr *goatherd.ScopeManager, scopeID string, res subagent.WorldResolution) {
	if mgr == nil || scopeID == "" {
		return
	}
	scope, ok := mgr.Get(scopeID)
	if !ok {
		return
	}
	if _, err := scope.AppendFact(ctx, "yaah.subagent.world.v1", "world", worldFactPayload(res)); err != nil {
		slog.Debug("world fact: append failed", "scope", scope.ID(), "err", err)
	}
}

// worldFactPayload is the recorded shape of a dispatch-world resolution. Kept
// separate so the ledger and a test can pin the same key set.
func worldFactPayload(res subagent.WorldResolution) map[string]any {
	return map[string]any{
		"requested": string(res.Requested),
		"resolved":  string(res.Resolved),
		"degraded":  res.Degraded,
		"reason":    res.Reason,
	}
}

// retainedNote appends a caveat to a result instead of replacing it: the
// deliverable stays readable and the caveat travels in the channel the
// orchestrator actually reads (yaah-atn).
func retainedNote(result, note string) string {
	note = strings.TrimSpace(note)
	if note == "" {
		return result
	}
	if strings.TrimSpace(result) == "" {
		return "note: " + note
	}
	return result + "\n\nnote: " + note
}

// retainedRunError is a report-lane body failure carrying the lane's caveat
// out of band. The caveat used to be appended to the result and parsed back out
// with a "note: " search, but the result is model output: a report that
// legitimately contains (or opens with) a "note: " line was cut there and the
// remainder re-labelled as the caveat. An error wrapper keeps the two apart.
//
// Error renders body failure plus caveat so a caller that only sees the error
// (the background lane delivers result and error separately) still keeps the
// caveat; Unwrap exposes the cause for errors.Is/As classification.
type retainedRunError struct {
	cause error
	note  string
}

func (e *retainedRunError) Error() string {
	if e.note == "" {
		return e.cause.Error()
	}
	return e.cause.Error() + "\n\nnote: " + e.note
}

func (e *retainedRunError) Unwrap() error { return e.cause }

// Note budgets for the report lane: a provisioning error is head-capped at
// its call site (its reason is at the front), and a caveat folded into a
// delegate error is tail-capped by foldRetainedPartial (a teardown note ends
// with the worktree path it reports).
const noteBudget = 4096

// foldRetainedPartial projects a failed operation's partial result into an
// error text. Both halves are bounded, because the loop delivers error text
// verbatim: the body keeps its head (the part that orients), the caveat
// keeps its tail (teardown notes end with the worktree path they report).
func foldRetainedPartial(body, note string) string {
	return retainedNote(truncateAt(body, toolResultMaxBytes), keepTail(note, noteBudget))
}

// worktreeOnDisk reports whether a worktree directory still exists.
func worktreeOnDisk(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// teardownReportScratch tears a report dispatch's scratch down and describes
// what it could not remove. A report dispatch registers no proposal, so a
// worktree that does not come down has no record to be recovered through
// later — the caller receives a note instead (yaah-atn). discard reports
// (handled, err); handled=false means the scope was already gone.
func teardownReportScratch(ctx context.Context, rt RetainedRuntime, wt string, discard func() (bool, error)) string {
	handled, err := discard()
	if !handled {
		// The scope went away before we could discard it. A worktree still
		// registered with the repo can be destroyed directly; one that is not
		// registered has no owner left to remove it, so it must be reported.
		if !recovery.IsRegisteredWorktree(ctx, rt.RepoPath, wt) {
			if !worktreeOnDisk(wt) {
				return ""
			}
			return fmt.Sprintf("the report dispatch could not discard its isolated scope; it left the worktree at %s", wt)
		}
		err = goatherdgit.NewWorktreeSandbox(rt.RepoPath, wt).Destroy(context.WithoutCancel(ctx))
		if err == nil {
			return ""
		}
	}
	if err == nil {
		return ""
	}
	if worktreeOnDisk(wt) {
		return fmt.Sprintf("worktree teardown failed: %v; the worktree is still on disk at %s", err, wt)
	}
	return fmt.Sprintf("worktree teardown reported an error (%v) but the worktree at %s is gone", err, wt)
}

// settleRetainedProposal settles one proposal with the given verb, once.
func (r *ProposalRegistry) settleRetainedProposal(ctx context.Context, id, action string) (string, error) {
	p, err := r.get(id)
	if err != nil {
		return "", err
	}
	if _, ok := settlementVerb(action); !ok {
		return "", fmt.Errorf("proposal: unknown action %q (select, apply, release, discard)", action)
	}
	if p.delta != nil {
		return r.settleGitDeltaProposal(ctx, p, action)
	}
	if p.output == nil {
		return "", fmt.Errorf("proposal %s has no sealed output to settle", id)
	}
	verb, _ := settlementVerb(action)

	env := proposalEnvelope{
		Status:     "settled",
		ProposalID: id,
		Action:     action,
		Files:      p.changedFiles(),
	}

	// Release integrates nothing and leaves the isolated scope — and its
	// worktree — untouched (the kernel's release is "keep, don't integrate").
	// Because the sealed delta never reaches the parent, export it to disk
	// BEFORE Settle consumes the sealed output; if the export fails, the
	// output is still intact and the release can be retried.
	if verb == goatherd.SettleRelease {
		retained, err := exportReleasedDelta(p)
		if err != nil {
			// Do NOT tear down on an export failure: the worktree still holds
			// the delta, so leaving it in place is safer than destroying it.
			return "", fmt.Errorf("proposal %s: release could not preserve the delta: %w", id, err)
		}
		env.Retained = retained
	}

	if err := p.output.Settle(ctx, verb); err != nil {
		switch {
		case errors.Is(err, goatherd.ErrOutputConsumed):
			return "", fmt.Errorf("proposal %s was already settled — a sealed output is consumed once", id)
		case errors.Is(err, goatherd.ErrApplyConflict):
			return "", fmt.Errorf("proposal %s: the parent changed paths this proposal also changes (%w); resolve the overlap, then apply or discard", id, err)
		case errors.Is(err, goatherd.ErrSettleEffectLanded):
			// The work is on the parent; only the causal merge failed to finish.
			// End the proposal with the failure noted — a retry would re-apply
			// an effect that already happened.
			env.Error = err.Error()
		default:
			return "", fmt.Errorf("proposal %s: %w", id, err)
		}
	}

	// Only after a successful release settle do we tear the child down;
	// select, apply, and discard already end the child scope themselves.
	if verb == goatherd.SettleRelease {
		if err := teardownReleasedProposal(p); err != nil {
			env.Notes = fmt.Sprintf("release worktree teardown failed: %v; worktree stranded at %s", err, p.workdir)
		}
	}

	env.Outcome = settleOutcome(verb, p, env).String()

	// Consume-once: a settled proposal leaves the registry.
	r.remove(id)
	return marshalProposalEnvelope(env), nil
}

// teardownReleasedProposal discards the isolated child scope a released
// proposal owned, removing its worktree. It returns an error when the scope
// could not be discarded, in which case the worktree is left stranded.
func teardownReleasedProposal(p *retainedProposal) error {
	if p.mgr == nil {
		return nil
	}
	parent, ok := p.mgr.Get(p.parentID)
	if !ok {
		return nil
	}
	scope, ok := p.mgr.Get(p.scopeID)
	if !ok {
		return nil
	}
	return parent.Discard(scope)
}

// exportReleasedDelta writes the sealed delta of a released proposal to a
// retention path next to its worktree, so release is honest about preserving
// the work before the worktree is torn down. It returns the path written.
func exportReleasedDelta(p *retainedProposal) (string, error) {
	if p.output == nil {
		return "", fmt.Errorf("no sealed output to export")
	}
	dir := filepath.Join(filepath.Dir(p.workdir), "releases")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, sanitizeRetainedID(p.id)+".release.patch")

	var b strings.Builder
	fmt.Fprintf(&b, "release record for proposal %s\n", p.id)
	if p.result != "" {
		fmt.Fprintf(&b, "\nresult:\n%s\n", p.result)
	}
	if files := changesPaths(p.output.Changes()); len(files) > 0 {
		fmt.Fprintf(&b, "\nfiles:\n%s\n", strings.Join(files, "\n"))
	}
	fmt.Fprintf(&b, "\ndiff:\n%s\n", p.output.Diff())

	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// settleOutcome renders the four-field Outcome for a settled proposal so the
// envelope carries "state, not completion" in one place (yaah-lka3).
func settleOutcome(verb goatherd.SettlementAction, p *retainedProposal, env proposalEnvelope) Outcome {
	oc := Outcome{
		Scope:  strings.Join(env.Files, ", "),
		Record: "proposal " + p.id,
		Notes:  env.Notes,
	}
	switch verb {
	case goatherd.SettleSelect:
		oc.Work = "worktree landed"
	case goatherd.SettleApply:
		oc.Work = "delta integrated into parent"
	case goatherd.SettleRelease:
		oc.Work = "delta exported and worktree torn down"
		oc.Record = "retained " + env.Retained
	case goatherd.SettleDiscard:
		oc.Work = "delta discarded"
		oc.Record = ""
	}
	return oc
}

// sanitizeRetainedID maps proposal ids to path-safe segment names (ids may
// contain colons and slashes, which are legal in memory but not on every
// filesystem).
func sanitizeRetainedID(id string) string {
	return strings.NewReplacer(":", "-", "/", "-", "\\", "-").Replace(id)
}

// settlementVerb maps a proposal verb to the kernel's settlement action.
func settlementVerb(action string) (goatherd.SettlementAction, bool) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "select":
		return goatherd.SettleSelect, true
	case "apply":
		return goatherd.SettleApply, true
	case "release":
		return goatherd.SettleRelease, true
	case "discard":
		return goatherd.SettleDiscard, true
	default:
		return "", false
	}
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

	sb := goatherdgit.NewWorktreeSandbox(rt.RepoPath, wtPath)
	if err := sb.Create(ctx, goatherdsandbox.SandboxSpec{}); err != nil {
		// A worktree left behind by a killed run can occupy the path. Only
		// clear it when it is actually a worktree registered with this repo.
		if recovery.IsRegisteredWorktree(ctx, rt.RepoPath, wtPath) {
			_ = sb.Destroy(context.WithoutCancel(ctx))
		}
		if retryErr := sb.Create(ctx, goatherdsandbox.SandboxSpec{}); retryErr != nil {
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
func changesPaths(changes []goatherd.ProposedChange) []string {
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
