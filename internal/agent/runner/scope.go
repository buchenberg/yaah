package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	goatherd "github.com/buchenberg/goatherd-runtime"
	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
	goatherdgit "github.com/buchenberg/goatherd-runtime/sandbox/git"
	"github.com/buchenberg/yaah/internal/jobs"
)

// subAgentSandbox returns the workspace substrate for a sub-agent: its own
// isolated worktree when the supervised session assigned one, otherwise the
// shared repository.
//
// In the isolated case the worktree already exists — the supervised session
// created it — so this sandbox is used for capture and apply only and never
// calls Create. A nil return leaves the scope pure-causal, which makes its
// workspace operations report ErrNoSandbox rather than silently doing nothing.
func subAgentSandbox(opts taskRunnerOpts, workdir string) goatherdsandbox.Sandbox {
	// Isolated session (plan §1.3, decision D2): the sub-agent shares the
	// session's one sandbox, so there is no per-sub-agent host substrate
	// to capture. A host worktree or host git sandbox would checkpoint a
	// tree this sub-agent never touches, and in-container checkpoints
	// could not isolate one sub-agent's turns from the parent's work in
	// the shared root — the same reason D2 rejects per-sub-agent
	// sandboxes. Nil keeps the scope pure-causal, so the supervisor's
	// inject/halt still works and turn checkpoints stay off rather than
	// rewinding the shared container.
	if opts.workspace != nil {
		return nil
	}
	repoPath := opts.defaults.RetainedRepoPath
	if repoPath == "" {
		repoPath, _ = os.Getwd()
	}
	if repoPath == "" {
		return nil
	}
	if workdir != "" {
		return goatherdgit.NewWorktreeSandbox(repoPath, workdir)
	}
	return goatherdgit.NewLocalGitSandbox(repoPath, goatherdgit.WithCleanPolicy(goatherdgit.CleanPreserveUntracked))
}

// ErrRootTerminal reports a recovered root that cannot be reattached
// because its lifecycle already ended (halted, discarded, or merged).
// Callers use it to make the degraded mode explicit instead of
// silently falling back to flat scopes.
var ErrRootTerminal = errors.New("recovered root scope is terminal")

// CreateRootScope creates the session's root scope (shepherd-adoption plan
// D1): the anchor every sub-agent scope forks from, owned by the session so
// scope IDs and trace owners line up. Local mode gets the shared in-place
// git sandbox — the same backend supervised sessions use, and never
// destroyed (the kernel's materializer contract). An isolated session gets
// a pure-causal root: the container is the substrate, and host-git capture
// would record a tree the session never works on (isolation plan D2). An
// empty repoPath falls back to the process working directory, matching
// subAgentSandbox's resolution so the root and legacy child scopes agree
// on the repository.
//
// The sandbox is probed with one capture before the root is registered:
// Fork captures the parent's baseline first, so a root over a directory
// git cannot capture (not a repository, unborn HEAD) would make every
// child fork fail — and the dispatch ignores that error, leaving all
// sub-agents without a scope. A failed probe demotes the root to
// pure-causal: the tree still exists, inject/halt still address every
// scope, and only checkpoint capture is unavailable — which it would
// have been anyway.
func CreateRootScope(mgr *goatherd.ScopeManager, sessionID, repoPath string, isolated bool) (*goatherd.Scope, error) {
	// Resume reattach (adoption plan R2): --resume continues under
	// the same session ID, so a recovered registry already holds the
	// crashed run's root — reattach it instead of colliding.
	// Recovery re-adopted its sandbox (or kept it pure-causal for an
	// isolated session, matching the mode the resolver was given), so
	// the scope tree resumes where it left off.
	if scope, ok := mgr.Get("scope:" + sessionID); ok {
		if scope.State() == goatherd.ScopeActive {
			return scope, nil
		}
		return nil, fmt.Errorf("%w: %s — it is terminal, so scope-tree supervision for this session ended; start a new session", ErrRootTerminal, scope.State())
	}
	var sb goatherdsandbox.Sandbox
	if !isolated {
		if repoPath == "" {
			repoPath, _ = os.Getwd()
		}
		if repoPath != "" {
			candidate := goatherdgit.NewLocalGitSandbox(repoPath, goatherdgit.WithCleanPolicy(goatherdgit.CleanPreserveUntracked))
			if _, cerr := candidate.Capture(context.Background()); cerr == nil {
				sb = candidate
			}
		}
	}
	return mgr.Create(sessionID, sb)
}

// createSubAgentScope creates the sub-agent's execution scope (adoption
// plan D1): a fork of the session's root scope, so the scope tree mirrors
// the dispatch tree and recovery can rebuild it. A workdir dispatch forks
// in isolation with its own worktree sandbox attached NON-owned
// (WithSandbox(sb, false), yaah-95ri.6): the fork record still carries
// the sandbox address for recovery and turn checkpoints still read it,
// but run-end Discard never destroys the worktree — it must outlive the
// run for recovery and, on the retained lane, for the proposal's seal.
// Destroying it is the teardown paths' job (container teardown,
// teardownReportScratch, retained settlement), never run-end
// completion's. Every other dispatch shares the root's sandbox. The fork
// snapshot is nil (D1): seed messages live in the loop config, not the
// trace. An empty root scope ID — tracing came up without a root — falls
// back to the
// legacy flat Create so the sub-agent still gets a superviseable scope.
func createSubAgentScope(mgr *goatherd.ScopeManager, opts taskRunnerOpts, subTraceID, workdir string) (*goatherd.Scope, error) {
	if mgr != nil {
		// Dispatch-scope cap (yaah-95ri.4): refuse before any fork — flat,
		// isolated or shared — so a saturated substrate fails the dispatch
		// fast with ErrScopeExhausted instead of provisioning a world that
		// will contend with (or hang behind) the scopes already present.
		cap := jobs.ResolveDispatchScopeCap(opts.maxDispatchScopes)
		if active := mgr.ActiveScopes(); len(active) >= cap {
			return nil, fmt.Errorf("%w: %d active scopes at cap %d", jobs.ErrScopeExhausted, len(active), cap)
		}
	}
	if opts.rootScopeID == "" {
		return mgr.Create(subTraceID, subAgentSandbox(opts, workdir))
	}
	// Post-halt dispatch policy (plan 07, H6): a root whose lifecycle
	// ended — the supervisor halt's target, A3's session-level kill —
	// refuses the dispatch loudly instead of silently degrading to a
	// flat scope outside the session tree. A racing dispatch that
	// landed there would be superviseable-but-orphaned: recovery
	// rebuilds it as a sibling of a dead session, and its scope ID
	// addresses nothing the session owns.
	//
	// The pre-check fails fast (the workdir path would provision a
	// worktree for a dispatch that cannot run), and the fork-failure
	// paths below RE-CHECK before degrading: a halt that lands between
	// the check and the fork must not slip through the TOCTOU window
	// into the flat-scope fallback. A root missing from the registry
	// is not this policy's case — the fork fails and the dispatch
	// degrades like any other non-terminal failure.
	root, ok := mgr.Get(opts.rootScopeID)
	if ok && root.State() != goatherd.ScopeActive {
		return nil, fmt.Errorf("%w: %s — the supervisor halt ended this session's supervision; start a new session", ErrRootTerminal, root.State())
	}
	// workdir isolates this dispatch in its own worktree — a local-mode
	// concept: an isolated session ignores it (its root has no sandbox,
	// so the fork is already pure-causal and the container's root is
	// every sub-agent's workspace). Without a resolvable repository the
	// worktree cannot be provisioned (subAgentSandbox returns nil in the
	// same degenerate case), so the dispatch degrades to a plain shared
	// fork rather than a worktree sandbox rooted at an empty path.
	if workdir != "" && opts.workspace == nil {
		repoPath := opts.defaults.RetainedRepoPath
		if repoPath == "" {
			repoPath, _ = os.Getwd()
		}
		if repoPath != "" {
			sb := goatherdgit.NewWorktreeSandbox(repoPath, workdir)
			child, ferr := mgr.ForkIsolated(opts.rootScopeID, subTraceID, nil, sb)
			if ferr == nil {
				// No yaah-side worktree record is needed (plan 06,
				// kernel v0.14.0): the kernel's fork record carries
				// the sandbox address itself, so recovery finds the
				// worktree from the trace alone.
				//
				// The scope must NOT own the sandbox (yaah-95ri.6):
				// WithSandbox(sb, false) keeps the sandbox address on
				// the fork record (recovery) and on the scope handle
				// (turn checkpoints read subScope.Sandbox()), while
				// the run-end Discard never destroys the worktree —
				// it must outlive the run for recovery and, on the
				// retained lane, for the proposal's seal. Destroying
				// it is teardown's job, never run-end completion's.
				child.WithSandbox(sb, false)
				return child, nil
			}
			// A halt that landed between the pre-check and this fork
			// must not slip through the TOCTOU window into the
			// fallback: re-check before degrading. A nil root is the
			// registry miss the pre-check let through — it degrades
			// like any other non-terminal failure.
			if root != nil && root.State() != goatherd.ScopeActive {
				return nil, fmt.Errorf("%w: %s — the supervisor halt ended this session's supervision; start a new session", ErrRootTerminal, root.State())
			}
			// A non-terminal ForkIsolated failure degrades loudly:
			// the sub-agent still runs, superviseable, but its escape
			// from the session tree is visible, not silent.
			slog.Warn("sub-agent dispatch: isolated fork failed; degrading to a flat scope outside the session tree",
				"root", opts.rootScopeID, "sub", subTraceID, "err", ferr)
			return mgr.Create(subTraceID, subAgentSandbox(opts, workdir))
		}
	}
	child, ferr := mgr.Fork(opts.rootScopeID, subTraceID, nil)
	if ferr == nil {
		return child, nil
	}
	// A halt that landed between the pre-check and this fork must not
	// slip through the TOCTOU window into the fallback: re-check
	// before degrading. A nil root is the registry miss the pre-check
	// let through — it degrades like any other non-terminal failure.
	if root != nil && root.State() != goatherd.ScopeActive {
		return nil, fmt.Errorf("%w: %s — the supervisor halt ended this session's supervision; start a new session", ErrRootTerminal, root.State())
	}
	// A non-terminal fork failure degrades loudly, the same contract:
	// superviseable, with the escape from the tree visible.
	slog.Warn("sub-agent dispatch: fork failed; degrading to a flat scope outside the session tree",
		"root", opts.rootScopeID, "sub", subTraceID, "err", ferr)
	return mgr.Create(subTraceID, subAgentSandbox(opts, workdir))
}
