package yaah

// wiring_assemble.go holds the assemble-stage concern split (yaah-mp04):
// isolated-workspace enforcement, prompt assembly, the D4 session-record
// write, the embedding backfill launch, session construction, and the
// post-construction hook wiring. Each is one concern; assemble() in
// wiring_stages.go sequences them and preserves their ordering contract.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/supervision"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/types"
)

// enforceIsolatedWorkspace hands the registry the sandbox workspace only after
// every tool is registered. SetWorkspace is the enforcement point — a refusal
// fails the session loudly rather than half-isolating it, so nothing below can
// believe a contained run is in effect when it is not. The destroy joins the
// error: a teardown failure means the container may still be running.
func enforceIsolatedWorkspace(ws *workspaceStage, tr *toolRegistryStage) error {
	if ws.sessionSandbox == nil {
		return nil
	}
	if err := tr.reg.SetWorkspace(ws.sessionSandbox.Workspace()); err != nil {
		// Teardown failure joins the returned error — a destroy that
		// failed means the container may still be running. The guard
		// is then stood down: reporting one failure on two channels
		// (and destroying twice through the once-guard) helps no one.
		err = fmt.Errorf("isolated workspace: %w", err)
		destroyCtx, cancelDestroy := context.WithTimeout(context.Background(), workspaceSandboxDestroyTimeout)
		defer cancelDestroy()
		if derr := ws.sessionSandbox.Destroy(destroyCtx); derr != nil {
			err = errors.Join(err, fmt.Errorf("workspace sandbox teardown: %w", derr))
		}
		ws.teardownHandled = true
		return err
	}
	return nil
}

// appendToolGuidance appends the model-facing guidance to the base prompt. The
// prompt must never advertise a tool that is not registered, so the tool
// sections come after the last registration — the belt's list is only final
// there. Isolated mode gets the §2.2 delta instead of the per-tool guidance.
func appendToolGuidance(opts SessionOptions, ws *workspaceStage, tr *toolRegistryStage, mcps *mcpSkillsStage, loop *loopInfraStage, beltRemoved []string) string {
	mainPrompt := loop.mainPrompt
	if ws.sessionSandbox != nil {
		slices.Sort(tr.omittedTools)
		mainPrompt += "\n\n" + prompts.IsolatedWorkspaceGuidance(tr.omittedTools, mcps.omittedMCP, beltRemoved)
	} else {
		// The guidance must never advertise a tool the belt excluded.
		if tr.reg.Get("delegate") != nil {
			mainPrompt += "\n\n" + prompts.SubAgentToolsGuidance()
		}
		// RetainedModeGuidance names the proposal tool, so it is gated on the
		// same signal as that tool's registration — taskTool.Retained — rather
		// than on the config key alone. A store-open failure or an
		// uncapturable root makes the lane unavailable, and advertising
		// settlement then would promise work the runtime refuses.
		if loop.taskTool.Retained != nil && tr.reg.Get("proposal") != nil {
			mainPrompt += "\n\n" + prompts.RetainedModeGuidance()
		}
		// The belt notice is a sentence, not a list: the exact set is in
		// the request's schemas.
		if len(beltRemoved) > 0 {
			mainPrompt += "\n\n" + prompts.ToolBeltGuidance(len(beltRemoved))
		}
	}
	if opts.OneShot {
		mainPrompt += "\n\n" + prompts.OneShotGuidance()
	}
	// Unknown allowlist names warn here, at prompt-assembly time, when
	// the belt's know/refuse accounting is final. unknownToolWarnings
	// keeps the configured belt's wording and adds persona names the
	// intersection dropped (yaah-21uk.7).
	for _, line := range unknownToolWarnings(tr) {
		fmt.Fprintln(os.Stderr, line)
	}
	return mainPrompt
}

// recordSessionWorkspace is the D4 write: the workspace substrate and the
// omitted tool names go into the session record, so "the model didn't use
// background_process" and "the model couldn't" stay distinguishable for any
// reader of the transcript. Written only after the SetWorkspace gate has
// passed — a run the gate refused never started, and must not overwrite the
// record of the last one that did. A resume that changed mode overwrites,
// describing the most recent actual run.
func recordSessionWorkspace(lc *sessionConfig, tr *toolRegistryStage, mcps *mcpSkillsStage, loop *loopInfraStage, beltRemoved []string) {
	if tr.db == nil {
		return
	}
	backend := lc.cfg.Workspace.Backend
	if backend == "" {
		backend = config.BackendLocal
	}
	recorded := append(slices.Clone(tr.omittedTools), mcps.omittedMCP...)
	recorded = append(recorded, beltRemoved...)
	slices.Sort(recorded)
	if werr := tr.db.UpdateSessionWorkspace(loop.sessionID, backend, strings.Join(recorded, ",")); werr != nil {
		fmt.Fprintf(os.Stderr, "warning: recording workspace metadata: %v\n", werr)
	}
}

// launchEmbeddingBackfill starts backfilling embeddings for memory entries
// and session messages created before the embedding server was configured, and
// heals rows left at a stale width after a model swap. It must run only after
// the last fallible step has passed: the startup ownership guard can no longer
// fire, so the backfill cannot race the guard's db.Close.
func launchEmbeddingBackfill(tr *toolRegistryStage) {
	if tr.db == nil || !tr.embedderAttached {
		return
	}
	// Memory and messages reconcile in separate goroutines, each with its own
	// window: a large message sweep must not be starved by the memory sweep (or
	// vice versa) under one shared 30s context.
	backfill := func(label string, run func(context.Context) (int, error)) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if n, err := run(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "warning: %s embedding backfill: %v\n", label, err)
			} else if n > 0 {
				fmt.Fprintf(os.Stderr, "embedded %d existing %s\n", n, label)
			}
		}()
	}
	backfill("memory", tr.db.ReconcileEmbeddings)
	backfill("session message", tr.db.ReconcileMessageEmbeddings)
}

// newSessionFromStages builds the agentSession from the completed stages. The
// wake engine is attached but not rehydrated here: durable watches are resumed
// by Start, after the driver has installed its view and its idle-wake policy.
// Rehydrating during construction would race both.
func newSessionFromStages(lc *sessionConfig, opts SessionOptions, ws *workspaceStage, tr *toolRegistryStage, mcps *mcpSkillsStage, loop *loopInfraStage, mainPrompt string) *agentSession {
	// Wrap the provider with OTel instrumentation if enabled.
	provider := wrapProviderWithOtel(lc.provider, lc.otelActive, lc.cfg.Observability.Otel.Verbose)

	wakeCtx, wakeCancel := context.WithCancel(context.Background())
	sess := &agentSession{
		wakeCtx:        wakeCtx,
		wakeCancel:     wakeCancel,
		cfg:            lc.cfg,
		opts:           opts,
		provider:       provider,
		providerName:   lc.providerName,
		modelName:      lc.modelName,
		persona:        lc.persona.Persona,
		promptLayers:   mcps.layers,
		promptMemory:   mcps.promptMemory,
		systemPrompt:   loop.systemPrompt,
		mainPrompt:     mainPrompt,
		identityEnd:    loop.identityEnd,
		toolReg:        tr.reg,
		db:             tr.db,
		mcpClients:     mcps.mcpClients,
		mcpInfos:       mcps.mcpInfos,
		mcpToolNames:   mcps.mcpToolNames,
		procMgr:        mcps.procMgr,
		sessionID:      loop.sessionID,
		messages:       loop.messages,
		msgIdx:         loop.msgIdx,
		otelShutdown:   lc.otelShutdown,
		tracker:        loop.tracker,
		backgroundJobs: loop.backgroundJobs,
		wakeMgr:        loop.wakeMgr,
		wakeHooks:      loop.wakeHooks,
		sessionSandbox: ws.sessionSandbox,
		worldStatus:    loop.worldStatus,
		cwd:            ws.cwd,
		steerCh:        loop.steerCh,
		rootScopeID:    loop.rootScopeID,
		runs:           loop.runs,
		scopes:         loop.scopeMgr,
		traceStore:     loop.traceStore,
		traceBus:       loop.traceBus,
		supervisor:     loop.supervisorSvc,
		reaper:         loop.reaper,
		egressBroker:   loop.egressBroker,
		followupCh:     loop.followupCh,
		closeCh:        make(chan struct{}),
	}
	if loop.wakeMgr != nil {
		sess.wakeMgr = loop.wakeMgr
		loop.wakeMgr.SetOnWake(sess.deliverWake)
	}
	sess.seedRestoredUsage(loop.usage)
	return sess
}

// wireSessionHooks installs the post-construction wiring: the root-scope
// bridge, the workspace-ask approval prompt, background sub-agent usage
// attribution, and the session-scoped sub-agent lifecycle events.
func wireSessionHooks(lc *sessionConfig, opts SessionOptions, ws *workspaceStage, loop *loopInfraStage, sess *agentSession) {
	// Root-scope bridge registration (adoption plan A2/A3): inject into
	// the root steers the session (its own steering channel); halting it
	// cancels the session's current run — the supervisor.halt_root
	// opt-in the SupervisorTool carries. Registered after the session
	// literal because the cancel is the session's method.
	if loop.rootScopeID != "" && loop.runs != nil {
		// The root registers no quiesce signal: its sandbox (in-place
		// git, or none at all in isolated mode) is never destroyed by a
		// halt, so there is nothing a still-running prompt could race.
		loop.runs.Register(loop.rootScopeID, supervision.Run{
			SteerCh: loop.steerCh,
			Cancel:  sess.HaltCurrentRun,
		})
	}

	// Ask fallback for out-of-workspace access: prompts route through
	// the session's approval UI (TUI/web) or stdin in plain REPL mode.
	if ws.pathValidator != nil && (opts.WorkspaceAsk || lc.cfg.Agent.Default.WorkspaceAsk) {
		ws.pathValidator.AskFn = sess.promptWorkspaceAccess
	}

	// Attribute completed background sub-agent usage to the session
	// total (session-scoped, so never lost across turns/runs) and to
	// the session's sub-agent counter, so orchestrator and sub-agent
	// spend are separately reportable.
	loop.backgroundJobs.OnUsage = func(u types.Usage) {
		sess.mu.Lock()
		sess.totalUsage.PromptTokens += u.PromptTokens
		sess.totalUsage.CompletionTokens += u.CompletionTokens
		sess.totalUsage.TotalTokens += u.TotalTokens
		sess.totalUsage.CachedPromptTokens += u.CacheTokens()
		sess.subUsage.PromptTokens += u.PromptTokens
		sess.subUsage.CompletionTokens += u.CompletionTokens
		sess.subUsage.TotalTokens += u.TotalTokens
		sess.subUsage.CachedPromptTokens += u.CacheTokens()
		sess.flushUsage()
		// Publish only when idle: while a foreground run is in flight its
		// totals are not yet folded into totalUsage (only subUsage grows via
		// onSubUsage), so an Agent = total - sub split would under-count. The
		// run-end publish covers the foreground; a background completion
		// between runs is consistent and safe to publish immediately.
		if !sess.runInFlight {
			sess.publishUsageLocked()
		}
		sess.mu.Unlock()
	}

	// Session-scoped sub-agent lifecycle events keep the UI's Subagents pane
	// honest for background jobs that outlive the loop that dispatched them.
	// While a loop is live its broker hooks win (no duplicate events); once
	// the loop clears them, these fallbacks deliver the completion directly
	// to the current view.
	loop.backgroundJobs.SetSessionHooks(
		func(id, role, model, prompt string, world jobs.SubAgentWorld) {
			sess.dispatchEvent(&agent.SubAgentStartEvent{
				SubAgentID:    id,
				Role:          role,
				Model:         model,
				Prompt:        prompt,
				World:         world.Resolved,
				WorldDegraded: world.Degraded,
				WorldReason:   world.Reason,
			})
		},
		func(id, role, model, prompt, result string, dur time.Duration, err string, world jobs.SubAgentWorld) {
			sess.dispatchEvent(&agent.SubAgentEndEvent{
				SubAgentID:    id,
				Role:          role,
				Model:         model,
				Prompt:        prompt,
				Duration:      dur,
				Error:         err,
				Result:        result,
				World:         world.Resolved,
				WorldDegraded: world.Degraded,
				WorldReason:   world.Reason,
			})
		},
	)
}
