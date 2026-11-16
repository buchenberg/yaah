// workflow_runner.go is the production default behind the runner seam in
// workflow_run.go (workflowRunRunner): the runner `yaah workflow run` and
// `yaah workflow resume` dispatch delegate nodes through when no session
// composition sits behind them.
//
// The composition is the smallest one that tells the truth about where its
// facts came from — the same three sources the one-shot CLI uses, in the same
// order:
//
//  1. config.Load + config.Validate: the provider table, the sub-agent
//     stanza, and the agent defaults (routing, budgets, spill policy).
//  2. Provider resolution: the provider a one-shot session would pick from
//     that config, with per-role routing still resolved by name out of the
//     same table, and the sub-agent override resolved the same way.
//  3. The store: memory.OpenDefault, so dispatch transcripts, the memory
//     tools a role declares, and the trace-owner mapping have a real parent
//     row to hang off instead of vanishing.
//
// What it deliberately does NOT invent (the reason the seam refused for so
// long is worth keeping):
//
//   - No provider. A config with no usable credential is refused by name
//     rather than dispatched on the stub that only fails at the first Send,
//     so the refusal names the missing fact instead of hiding it behind a
//     dispatch error.
//   - No sandbox. The dispatch runs in the session's local posture — the
//     process working directory, the harness paths guarded, no container —
//     because a workflow run has no --workspace flag to honor and a container
//     started here would be a world the user never asked for.
//   - No supervision scope tree. Scopes fork from a session's root scope;
//     there is no session, so a dispatch runs without one rather than under a
//     scope the trace cannot find later.
//
// One thing the run DOES compose when the document asks for it: the retained
// proposal runtime. A document that declares `lane: proposal` is choosing the
// retained lane, and refusing it would make the fanout_settle shape — the
// speculative pattern the DSL exists for — unrunnable standalone. The runtime
// is composed from the same facts a session composes it from (the shepherd
// trace store, a root scope over the working tree, and a per-run proposal
// registry), created lazily once the first proposal-lane dispatch needs it,
// and torn down in the cleanup the seam already returns. This is not an
// invented fact: the document declared the lane, and the runtime is the
// machinery that lane requires.
package yaah

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/agent/runner"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/dispatch"
	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/tools"
)

// newStandaloneWorkflowRunner composes the workflow commands' dispatch runner
// from the user's configuration and nothing else. It returns the teardown the
// command owes whatever it opened (today the store; a no-op once the run is
// over and every handle is released), so the command — not this constructor —
// owns the lifetime. Every refusal names the fact that was missing.
//
// The retained runtime, when the run needs one, is composed here too — see
// standaloneRetained() — and torn down in the same cleanup.
func newStandaloneWorkflowRunner() (jobs.TaskRunner, func(), error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("workflow: %w", err)
	}
	if verr := config.Validate(cfg); verr != nil {
		return nil, nil, fmt.Errorf("workflow: config %s: %w", configPathLabel(), verr)
	}

	// Provider resolution comes before anything else is built: a machine with
	// no usable provider fails here, before a store is opened or the
	// process-wide role registry is rebuilt, so the refusal leaves no
	// footprint.
	providerName := resolveProviderName(cfg)
	provider := resolveProvider(cfg)
	if _, none := provider.(*providers.NoProviderStub); none || provider == nil {
		return nil, nil, fmt.Errorf("no provider configured: %s holds no provider with a usable credential, and a workflow run will not invent one — add a provider there (or point agents.default.provider at a usable entry) and check it with 'yaah doctor'", configPathLabel())
	}

	// Workspace: the working directory and the role registry the dispatch
	// resolves roles against. buildWorkspace is the session's stage for this
	// and starts nothing — the containment substrate is a separate stage,
	// deliberately not run here (see the sandbox note above).
	ws, werr := buildWorkspace(cfg, SessionOptions{})
	if werr != nil {
		return nil, nil, fmt.Errorf("workflow: %w", werr)
	}

	modelName := resolveModel(cfg)
	subProvider, subModel := resolveSubAgent(cfg)
	contextWindow := providers.ResolveWindow(modelName, cfg.Agent.Default.ContextWindow) / 2
	if contextWindow < 32000 {
		contextWindow = 32000
	}
	db, sessionID := openWorkflowDispatchStore(ws.cwd, providerName, modelName)

	run, rerr := runner.NewStandaloneTaskRunner(runner.StandaloneOpts{
		Cfg:                   cfg,
		Provider:              provider,
		ProviderName:          providerName,
		ModelName:             modelName,
		SystemPrompt:          buildSubAgentBasePrompt(ws.cwd, db),
		DB:                    db,
		SessionID:             sessionID,
		SubAgentProvider:      subProvider,
		SubAgentModel:         subModel,
		SubContextWindow:      contextWindow,
		PathValidator:         standalonePathValidator(ws),
		ToolSpillDir:          toolSpillDir(),
		ResolveProviderByName: resolveProviderByName,
	})
	if rerr != nil {
		if db != nil {
			db.Close()
		}
		return nil, nil, rerr
	}

	// The retained runtime, when the document needs one. Composed lazily —
	// a report-only document never pays for the shepherd infrastructure —
	// but composed HERE, before the run starts, so a composition failure
	// is a named refusal at the door rather than a mid-run dispatch error.
	retained, retainedCleanup, rterr := standaloneRetained(cfg, ws, run, sessionID)
	if rterr != nil {
		if db != nil {
			db.Close()
		}
		return nil, nil, rterr
	}

	cleanup := func() {}
	if db != nil || retainedCleanup != nil {
		cleanup = func() {
			if retainedCleanup != nil {
				retainedCleanup()
			}
			if db != nil {
				db.Close()
			}
		}
	}
	workflowRetainedRuntime.Store(retained)
	return run, cleanup, nil
}

// workflowRetainedRuntime carries the retained runtime this process's
// workflow run composed, for the run/resume commands to hand to the door.
// It is process-scoped because the seam is a package var: the run command
// composes once per process invocation, and the resume command reads it
// after the seam's default runs. Nil means the run composed no retained
// runtime (a report-only document, or a composition that refused).
var workflowRetainedRuntime atomic.Pointer[dispatch.RetainedRuntime]

// standaloneRetained composes the retained proposal runtime for a standalone
// workflow run: the same four facts a session composes it from, minus the
// session. A document that declares `lane: proposal` needs this machinery —
// the worktree fork, the scope fork, and the registry settle nodes dispose
// through — so the composition mirrors the session's rather than inventing a
// weaker one. It is built lazily: only when the document actually declares a
// proposal-lane dispatch, so a report-only run pays nothing.
//
// The honesty rule survives: the runtime is composed from the user's config
// (the trace path, the retained repo path, the worktree root) and the process
// working tree, and a composition that cannot be completed is a named refusal
// — a run that cannot settle its own proposals must not seal them.
func standaloneRetained(cfg *config.Config, ws *workspaceStage, run jobs.TaskRunner, sessionID string) (*dispatch.RetainedRuntime, func(), error) {
	// The graph's lane needs are a static fact of the resolved document —
	// known before any dispatch runs. But the seam composes the runner
	// before the graph resolves (the command resolves the document after
	// the seam call), so the runtime is composed unconditionally and the
	// door refuses by name when a proposal-lane node meets a nil runtime.
	// Composing unconditionally also keeps the resume path honest: resume
	// reads the runtime this process composed for the run it continues.
	traceStore, traceBus, mgr, serr := pipeline.InitShepherdInfrastructure(cfg.Agent.Default.TracePath(), 0)
	if serr != nil {
		return nil, nil, fmt.Errorf("workflow: retained lane unavailable: %w", serr)
	}
	repoPath := cfg.Agent.Default.RetainedRepoPath
	if repoPath == "" {
		repoPath = ws.cwd
	}
	root, rerr := runner.CreateRootScope(mgr, sessionID, repoPath, false)
	if rerr != nil {
		pipeline.CloseShepherdInfrastructure(traceStore, traceBus)
		return nil, nil, fmt.Errorf("workflow: retained lane unavailable: root scope: %w", rerr)
	}
	rt := &dispatch.RetainedRuntime{
		Runner:       run,
		RepoPath:     repoPath,
		ScopeManager: mgr,
		RootScopeID:  root.ID(),
		SessionID:    sessionID,
		WorktreeRoot: cfg.Agent.Default.RetainedWorktreeRoot,
		Proposals:    dispatch.NewProposalRegistry(),
	}
	rt.EnsureInFlight()
	cleanup := func() {
		pipeline.CloseShepherdInfrastructure(traceStore, traceBus)
	}
	return rt, cleanup, nil
}

// configPathLabel names the configuration file in a refusal. A home that will
// not resolve is not worth failing the refusal over: the message says "the
// yaah configuration" instead.
func configPathLabel() string {
	path, err := config.ConfigPath()
	if err != nil {
		return "the yaah configuration"
	}
	return path
}

// standalonePathValidator mirrors the session's local substrate with no
// --workspace: unrestricted on the host (home paths reachable, no deny
// patterns — the workflow run has no workspace flag to derive them from), but
// still holding the harness guard, so a dispatched sub-agent can no more
// rewrite ~/.yaah/config.yaml or a role file than a session turn can.
func standalonePathValidator(ws *workspaceStage) *tools.PathValidator {
	pv := tools.NewPathValidator("", true, nil)
	pv.WorkDir = ws.cwd
	pv.Harness = ws.harness
	return pv
}

// openWorkflowDispatchStore opens the store the run's dispatches record into
// and mints the parent id they stay joinable to. Both degrade rather than
// fail, the way the session's store open does: a store that will not open
// only costs persistence, and a run that cannot persist is still a run.
//
// The handle lives until the command's teardown runs: the seam returns a
// cleanup alongside the runner precisely so the command, which knows when the
// run is over, owns the store's lifetime rather than leaking it to process
// exit (a leaked handle also pins the file on Windows, where a temp home
// cannot then be removed).
func openWorkflowDispatchStore(cwd, providerName, modelName string) (*memory.DB, string) {
	db, err := memory.OpenDefault()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: workflow: dispatch store unavailable, persistence disabled: %v\n", err)
		return nil, ""
	}
	id := fmt.Sprintf("workflow-%d", time.Now().UnixNano())
	if cerr := db.CreateSession(memory.Session{
		ID:        id,
		StartedAt: time.Now().Unix(),
		CWD:       canonicalStoredCwd(cwd),
		Model:     wireModelValue(providerName, modelName),
	}); cerr != nil {
		fmt.Fprintf(os.Stderr, "warning: workflow: dispatch session row failed, persistence disabled: %v\n", cerr)
		db.Close()
		return nil, ""
	}
	return db, id
}
