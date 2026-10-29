package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherd "github.com/buchenberg/goatherd-runtime"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/jobs"
)

// dispatch_container.go implements per-dispatch container worlds (yaah-ihfh.3).
// A report dispatch never seals, so its container is created, used, and
// destroyed. A proposal dispatch seals its work as a GitDelta manifest before
// the container is destroyed (docs/shepherd-parity.md §Container settlement:
// GitDelta), so settlement needs no live world: the manifest is the proposal.
// Both lanes obtain the world from the same DispatchWorlds setup.

const (
	// dispatchWorldStartTimeout bounds a per-dispatch container's preflight:
	// the image-contract probe (its own throwaway sandbox), Create, and the
	// copy-in bootstrap. It matches the session container's bound, large
	// enough for a first-run image pull but finite so a wedged daemon cannot
	// hang a dispatch forever.
	dispatchWorldStartTimeout = 15 * time.Minute

	// dispatchWorldDestroyTimeout bounds the container's teardown so a
	// finished (or failed) dispatch cannot hang on a wedged daemon on its way
	// out. It matches the session sandbox's teardown bound.
	dispatchWorldDestroyTimeout = 30 * time.Second
)

// dispatchWorldPreflightFailure decides what a failed container preflight means
// for the dispatch. An explicit `container` request fails closed: the operator
// asked for a world this host could not provide, and running elsewhere would
// silently disobey the config. `auto` steps down to the worktree, recording the
// failure as a degradation so the envelope and the trace say the dispatch ran
// in a different world than the ladder chose.
//
// It is pure, so the decision is testable without a daemon or a repository.
func dispatchWorldPreflightFailure(res subagent.WorldResolution, cause error) (subagent.WorldResolution, error) {
	if res.Requested == subagent.WorldContainer {
		return res, fmt.Errorf("delegate: per-dispatch container world: %w", cause)
	}
	res.Resolved = subagent.WorldWorktree
	res.Degraded = true
	res.Reason = "per-dispatch container world unavailable: " + truncateAt(cause.Error(), noteBudget)
	return res, nil
}

// openDispatchWorld runs the container preflight for one dispatch: probe the
// image contract, create the sandbox, and seed it. In a host session the
// source is the host repository; in a container session it is the session
// container's captured state, so a sibling sees the session's current world
// rather than a stale host copy. It returns a live sandbox on success; every
// failure path destroys whatever was opened.
func openDispatchWorld(ctx context.Context, rt RetainedRuntime) (*SessionSandbox, error) {
	dw := rt.DispatchWorlds
	if dw == nil || dw.Opener == nil {
		return nil, fmt.Errorf("no per-dispatch container setup is wired for this session")
	}
	startCtx, cancel := context.WithTimeout(ctx, dispatchWorldStartTimeout)
	defer cancel()

	if dw.SessionSandbox != nil {
		if dw.SessionSandbox.sb == nil {
			return nil, fmt.Errorf("the container session has no sandbox to seed from")
		}
		// A sibling is seeded from the session's snapshot, not the host tree:
		// edits made in the session container are part of the dispatch's
		// world, and the host copy may be stale.
		//
		// Probe releasability *before* Capture: Capture commits and pins a
		// layer, so a guard afterwards would refuse after the pin already
		// exists. The croft adapter implements ReleaseState; the fake does so
		// as a no-op; any other isolated backend must provide one.
		releaser, hasRelease := dw.SessionSandbox.sb.(interface {
			ReleaseState(context.Context, shepherdtrace.WorkspaceState) error
		})
		if !hasRelease && dw.SessionSandbox.sb.Capabilities().Isolated {
			return nil, fmt.Errorf("session backend %q captures state but does not expose ReleaseState", dw.SessionSandbox.sb.Backend())
		}
		state, err := dw.SessionSandbox.Capture(startCtx)
		if err != nil {
			return nil, fmt.Errorf("capture the session container state: %w", err)
		}
		release := func() error {
			// Capture pins a state. The sibling's active layer keeps its own
			// lease; drop this transient pin once the state has been applied.
			if !hasRelease {
				return nil
			}
			return releaser.ReleaseState(context.WithoutCancel(startCtx), state)
		}
		ss, err := StartSessionSandbox(startCtx, dw.Config, dw.Opener, "", dw.Deny)
		if err != nil {
			_ = release()
			return nil, err
		}
		if err := ss.sb.Apply(startCtx, state); err != nil {
			dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), dispatchWorldDestroyTimeout)
			defer dcancel()
			destroyErr := ss.Destroy(dctx)
			releaseErr := release()
			return nil, errors.Join(fmt.Errorf("seed the sibling container from the session state: %w", err), destroyErr, releaseErr)
		}
		if err := release(); err != nil {
			dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), dispatchWorldDestroyTimeout)
			defer dcancel()
			_ = ss.Destroy(dctx)
			return nil, fmt.Errorf("release the session capture: %w", err)
		}
		return ss, nil
	}

	if rt.RepoPath == "" {
		return nil, fmt.Errorf("no repository to seed the dispatch container from")
	}
	return StartSessionSandbox(startCtx, dw.Config, dw.Opener, rt.RepoPath, dw.Deny)
}

// runDispatchContainer runs one report dispatch inside its own container world.
// The child scope is pure-causal (no worktree sandbox): the container is the
// substrate, and it is owned by this call. A report registers no proposal, so
// there is nothing to settle and the container is destroyed on every path.
//
// The body runs with the sandbox's workspace in its context so the runner builds
// the sub-agent's registry against the container; teardown discards the scope
// and destroys the container however the body ends. Teardown problems travel as
// a report caveat rather than a log line: a report registers no proposal, so the
// result is the only channel the orchestrator reads.
func runDispatchContainer(
	ctx context.Context,
	rt RetainedRuntime,
	parent *goatherd.Scope,
	prompt string,
	subParams jobs.SubAgentParams,
	timeout time.Duration,
	res subagent.WorldResolution,
	sandbox *SessionSandbox,
) (result string, runErr error) {
	id := fmt.Sprintf("proposal:%d:%d", time.Now().UnixNano(), proposalSeq.Add(1))
	scope, err := rt.ScopeManager.Fork(rt.RootScopeID, id, nil)
	if err != nil {
		ferr := fmt.Errorf("delegate: fork container dispatch scope: %w", err)
		if note := teardownDispatchWorld(ctx, nil, nil, sandbox); note != "" {
			ferr = fmt.Errorf("%w; %s", ferr, note)
		}
		return "", ferr
	}
	recordWorldFact(ctx, rt.ScopeManager, scope.ID(), res)

	// Deferred so no return path — a body error, a supervisor halt, a panic —
	// leaves the container running. Both steps are bounded and cancel-free:
	// the dispatch's context is usually over by the time this runs, and a
	// teardown that honoured it would leak the very thing it exists to remove.
	defer func() {
		note := teardownDispatchWorld(ctx, parent, scope, sandbox)
		if note == "" {
			return
		}
		if runErr != nil {
			runErr = &retainedRunError{cause: runErr, note: note}
			return
		}
		result = retainedNote(result, note)
	}()

	sp := subParams
	sp.Workdir = sandbox.Workspace().WorkDir()

	runCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	runCtx = WithDispatchWorkspace(runCtx, sandbox.Workspace())

	return rt.Runner(runCtx, prompt, sp)
}

// runDispatchContainerProposal runs one proposal dispatch inside its own
// container world and seals the result as a GitDelta manifest. The container is
// destroyed at seal on every path — success, body error, or seal failure — so
// no world outlives the proposal and no lease or recovery scan is needed.
//
// A body error still yields a sealed (partial) manifest, matching the worktree
// lane: the work is reviewable, and the envelope says why the run stopped. A
// seal or persistence failure has no proposal to settle and is reported in the
// envelope, never silently dropped.
func runDispatchContainerProposal(
	ctx context.Context,
	rt RetainedRuntime,
	parent *goatherd.Scope,
	prompt string,
	subParams jobs.SubAgentParams,
	timeout time.Duration,
	res subagent.WorldResolution,
	sandbox *SessionSandbox,
	basisState shepherdtrace.WorkspaceState,
	basisDigest string,
) (out string, runErr error) {
	id := fmt.Sprintf("proposal:%d:%d", time.Now().UnixNano(), proposalSeq.Add(1))
	env := proposalEnvelope{ProposalID: id, Next: proposalVerbs(), World: string(res.Resolved)}
	if res.Degraded {
		env.Warning = "dispatch world degraded: " + res.Reason
	}

	var scope *goatherd.Scope
	// The container must be gone by the time the envelope is returned. Both
	// teardown steps are bounded and cancel-free: the dispatch's context is
	// usually over by now, and a teardown that honoured it would leak the very
	// thing it exists to remove.
	defer func() {
		if note := teardownDispatchWorld(ctx, parent, scope, sandbox); note != "" {
			env.Notes = appendSettleNote(env.Notes, note)
		}
		out = marshalProposalEnvelope(env)
	}()

	scope, err := rt.ScopeManager.Fork(rt.RootScopeID, id, nil)
	if err != nil {
		env.Status = "error"
		env.Error = "fork container dispatch scope: " + err.Error()
		return "", nil
	}
	recordWorldFact(ctx, rt.ScopeManager, scope.ID(), res)

	root := sandbox.Workspace().WorkDir()
	// The basis is captured after the bootstrap and before the body: the
	// bootstrap commits a synthetic baseline when the host tree was dirty, so
	// HEAD is exactly the tree the body starts from.
	base, herr := containerGitHead(ctx, sandbox.sb, root)
	if herr != nil {
		env.Status = "error"
		env.Error = "container dispatch: capture baseline HEAD: " + herr.Error()
		return "", nil
	}

	sp := subParams
	sp.Workdir = root

	budgetDeadline := time.Now().Add(timeout)
	parentDeadline, parentHasDeadline := ctx.Deadline()
	budgetGoverns := timeout > 0 && (!parentHasDeadline || parentDeadline.After(budgetDeadline))

	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	runCtx = WithDispatchWorkspace(runCtx, sandbox.Workspace())
	result, bodyErr := rt.Runner(runCtx, prompt, sp)
	runErrState := runCtx.Err()
	parentErr := ctx.Err()
	if cancel != nil {
		cancel()
	}

	var notes []string
	bodyFailure := ""
	if bodyErr != nil {
		source := classifyRetainedBodyFailure(bodyErr, timeout, runErrState, parentErr, budgetGoverns)
		bodyFailure = retainedBodyFailure(bodyErr, sp.Role, timeout, source)
		notes = append(notes, bodyFailure)
	}

	// Seal on the dispatch context, not the (possibly expired) run context: a
	// timed-out body's partial work is still worth sealing.
	delta, review, sealErr := captureGitDelta(ctx, sandbox.sb, root, base)
	if sealErr != nil {
		notes = append(notes, "seal: "+sealErr.Error())
		env.Status = "error"
		env.Result = trimResult(result)
		env.Error = trimResult(strings.Join(notes, "; "))
		return "", nil
	}
	dir, manifestDigest, perr := persistGitDelta(ctx, rt.RepoPath, id, delta, review, result, bodyFailure, rt.SessionID, rt.RootScopeID, basisState, basisDigest)
	if perr != nil {
		notes = append(notes, "seal: persist delta: "+perr.Error())
		env.Status = "error"
		env.Result = trimResult(result)
		env.Error = trimResult(strings.Join(notes, "; "))
		return "", nil
	}
	if _, ferr := parent.AppendFact(ctx, SchemaProposalSealed, "proposal", map[string]any{
		"proposal_id":     id,
		"world":           string(subagent.WorldContainer),
		"manifest_digest": manifestDigest,
		"files":           delta.Paths(),
		"basis_digest":    basisDigest,
		"artifact_dir":    dir,
	}); ferr != nil {
		// The manifest is durable; a missing seal fact is a record gap, not a
		// reason to throw the work away. Carry it to the caller visibly.
		notes = append(notes, "trace: seal fact: "+ferr.Error())
	}

	rt.Proposals.add(&retainedProposal{
		id:          id,
		delta:       delta,
		review:      review,
		result:      trimResult(result),
		runErr:      bodyFailure,
		parentID:    rt.RootScopeID,
		mgr:         rt.ScopeManager,
		repoPath:    rt.RepoPath,
		basisState:  basisState,
		basisDigest: basisDigest,
		artifactDir: dir,
		deltaDigest: manifestDigest,
		world:       string(subagent.WorldContainer),
	})

	env.Status = "proposal"
	env.Result = trimResult(result)
	env.Files = delta.Paths()
	reviewText := review
	if reviewText == "" {
		reviewText = delta.RenderDiff()
	}
	env.Diff = clipDiff(reviewText)
	if len(notes) > 0 {
		env.Error = trimResult(strings.Join(notes, "; "))
	}
	return "", nil
}

// teardownDispatchWorld discards the dispatch's pure-causal scope and destroys
// its container. It returns a caveat naming what it could not remove, or "".
func teardownDispatchWorld(ctx context.Context, parent, scope *goatherd.Scope, sandbox *SessionSandbox) string {
	var notes []string
	if parent != nil && scope != nil {
		if err := parent.Discard(scope); err != nil {
			notes = append(notes, "dispatch scope discard failed: "+err.Error())
		}
	}
	if sandbox != nil {
		destroyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dispatchWorldDestroyTimeout)
		defer cancel()
		if err := sandbox.Destroy(destroyCtx); err != nil {
			notes = append(notes, "dispatch container teardown failed: "+err.Error())
		}
	}
	if len(notes) == 0 {
		return ""
	}
	return strings.Join(notes, "; ")
}
