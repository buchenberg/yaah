package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/agent/events"
	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/permissions"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// toolDispatch owns the full lifecycle of exactly one tool call:
// concurrency gating, events, watchdog, execution, truncation, and
// result delivery. Extracted from the former ~270-line closure in
// executeAndCollect so each branch is individually testable and
// quality gates can reuse the same supervised path (review A1/B8).
type toolDispatch struct {
	loop *Loop
	idx  int
	call types.ToolCall

	toolID      int64
	abbreviated string

	isDelegate   bool
	taskRole     string
	taskPrompt   string
	isBackground bool

	subAgentID    string
	subAgentModel string
	// world is the resolved dispatch world, written by the retained runtime
	// (or seeded as "shared" by the direct lane) through the context pointer
	// installed in setupSubAgent.
	world      tools.SubAgentWorld
	subUsage   types.Usage
	startOnce  sync.Once
	finishOnce sync.Once

	watchdogActive bool
	// watch holds the liveness evidence the stuck-child watchdog needs to
	// classify a kill (world readiness, heartbeats, the resolved world).
	watch watchdogState
	// watchDone ends the watchdog goroutine when the dispatch returns,
	// so a body that exits without ever signalling readiness cannot leak
	// the goroutine for the rest of the parent's context lifetime.
	watchDone       chan struct{}
	releaseSubAgent func()
	releaseTool     func()
}

// newDispatch prepares the per-call state for one tool call.
func (l *Loop) newDispatch(idx int, call types.ToolCall) *toolDispatch {
	d := &toolDispatch{loop: l, idx: idx, call: call}
	d.toolID = l.toolIDGen.Add(1)
	d.abbreviated = abbreviateArgs(call.Function.Arguments, 80)
	d.isDelegate = call.Function.Name == "delegate"
	if d.isDelegate {
		d.taskRole, d.taskPrompt, d.isBackground = parseTaskArgs(call.Function.Arguments)
		d.subAgentID = fmt.Sprintf("sa-%d", l.subAgentIDGen.Add(1))
	}
	return d
}

// run executes the call end-to-end and delivers the result on results.
func (d *toolDispatch) run(ctx context.Context, results chan<- toolExecResult) {
	l := d.loop
	observability.RecordToolGoroutine(ctx, d.call.Function.Name, "spawned")

	if err := d.acquire(ctx); err != nil {
		results <- toolExecResult{idx: d.idx, callID: d.call.ID, name: d.call.Function.Name, args: d.call.Function.Arguments, content: "cancelled", err: err}
		return
	}
	defer d.release()

	if l.broker != nil {
		observability.RecordToolGoroutine(ctx, d.call.Function.Name, "publish_start")
		l.broker.PublishMustDeliver(&events.ToolStartEvent{ID: d.toolID, CallID: d.call.ID, Name: d.call.Function.Name, Args: d.abbreviated, RawArgs: d.call.Function.Arguments})
		observability.RecordToolGoroutine(ctx, d.call.Function.Name, "published")
	}

	// The sub-agent start event is emitted by the runner (via the start
	// notifier set up in setupSubAgent) once the child's model is
	// resolved, so views learn the role and model together. startOnce
	// guards a post-execution fallback for runners that never announce.
	publishStart := d.publishStartFn()

	l.Hooks.Emit(HookEvent{
		Event:    events.ToolStart,
		ToolName: d.call.Function.Name,
		ToolArgs: d.call.Function.Arguments,
	})

	start := time.Now()
	// One admission decision for the whole dispatch: the advertised tool
	// level plus plan mode's per-call danger check. The approval middleware
	// asks the same questions, but it is optional
	// (middleware.disabled: [approval]) — a mode that promises mutating
	// actions are withheld cannot be switched off by config.
	allowed, refusal := l.admitTool(d.call.Function.Name, d.call.Function.Arguments)

	runCtx := ctx
	// Delegate inheritance (parity W5.7): foreground and background
	// dispatches carry the loop's inherited ceiling — its ruleset filtered
	// to deny rules on mutating file tools — so the sub-agent's gates
	// enforce what the parent denied regardless of which role runs. The
	// Ceiling is computed per dispatch from this loop's live config, so a
	// persona switch that rewrote the ruleset is honored by the next
	// dispatch without rebuilding the delegate tool. Background jobs
	// inherit too: a deferred dispatch must not become the loophole.
	if d.isDelegate {
		runCtx = permissions.WithInherited(runCtx, l.inheritedCeiling())
	}
	var toolSpan trace.Span
	if l.Config.OtelEnabled {
		if allowed && d.isDelegate && !d.isBackground {
			runCtx, toolSpan = observability.StartSubAgent(ctx, d.taskRole, d.taskPrompt)
		} else if allowed && d.isDelegate && d.isBackground {
			// A short span for the dispatch only; the background job
			// owns its own long-lived sub-agent span (created by the
			// BackgroundJobs manager as a child of this).
			runCtx, toolSpan = observability.StartTool(ctx, "delegate:dispatch", d.call.Function.Arguments)
		} else {
			runCtx, toolSpan = observability.StartTool(ctx, d.call.Function.Name, d.call.Function.Arguments)
		}
	}

	callID := d.call.ID
	if callID == "" {
		callID = fmt.Sprintf("t%d", d.toolID)
	}
	runCtx = tools.WithToolCallID(runCtx, callID)

	if allowed && d.isDelegate && !d.isBackground {
		runCtx = d.setupSubAgent(runCtx)
		// The child accumulates usage on runCtx; fold it into the loop's
		// totals and the session's sub-agent share once the call returns.
		// Without this, foreground delegate spend is silently dropped from
		// state.db (sub_tokens_*) and OnSubUsage never fires. The explicit
		// fold below runs before the result is published; the defer covers
		// only early returns and is idempotent.
		defer d.finishSubAgent()
	}

	var supervisionMeta map[string]any
	runCtx = tools.WithSupervisionMeta(runCtx, &supervisionMeta)
	var res string
	var err error
	if allowed {
		res, err = l.Registry.Execute(runCtx, d.call.Function.Name, d.call.Function.Arguments)
	} else {
		err = refusal
	}

	if allowed && d.isDelegate && !d.isBackground {
		// Fallback for runners that never fired the start notifier: emit
		// the start event with the final model so views still see the
		// role/model pairing (arrives late but complete). Background
		// sub-agents are announced by the BackgroundJobs manager.
		d.startOnce.Do(func() {
			model := d.subAgentModel
			if model == "" {
				model = l.Config.Model
			}
			publishStart(model)
		})
	}

	// Parse structured escalation from raw sub-agent output before the
	// result is truncated for display. Background dispatch returns a
	// {"status":"running"} placeholder with no payload to parse.
	var escalation *tools.Escalation
	if allowed && d.isDelegate && !d.isBackground && l.broker != nil {
		if output := tools.ParseSubAgentOutput(res, err); output.Escalation != nil {
			escalation = output.Escalation
		}
	}

	// Watchdog kill: delegate already converted the cancellation into a
	// structured {"status":"error","stop_reason":"canceled",...} envelope. Keep that
	// payload so the child's partial output survives and surface the kill
	// classification alongside it (yaah-1ep). The verdict distinguishes a
	// child that went silent mid-run (stuck) from one that never started
	// after its world was ready (dispatch startup, yaah-95ri.3).
	killPartial := ""
	if allowed && d.isDelegate && !d.isBackground && d.watchdogActive && ctx.Err() == nil && runCtx.Err() == context.Canceled {
		err = d.killVerdict()
		killPartial = l.truncateToolResult(res)
	}
	if l.Config.OtelEnabled && toolSpan != nil {
		if allowed && d.isDelegate && !d.isBackground {
			observability.FinishSubAgent(toolSpan, err)
		} else {
			observability.FinishTool(toolSpan, res, err)
		}
		toolSpan.End()
	}
	duration := time.Since(start)

	errStr := ""
	if err != nil {
		errStr = err.Error()
		res = fmt.Sprintf("error: %v", err)
		if killPartial != "" {
			res += "\n" + killPartial
		}
	} else {
		res = l.truncateToolResult(res)
	}

	observability.RecordToolCall(runCtx, d.call.Function.Name, duration, err != nil)

	if l.broker != nil {
		evt := &events.ToolEndEvent{
			ID:       d.toolID,
			CallID:   d.call.ID,
			Name:     d.call.Function.Name,
			Args:     d.abbreviated,
			RawArgs:  d.call.Function.Arguments,
			Result:   res,
			Duration: duration,
		}
		if err != nil {
			evt.Error = err.Error()
		}
		l.broker.PublishMustDeliver(evt)
	}

	if allowed && d.isDelegate && !d.isBackground && l.broker != nil {
		d.emitSubAgentEnd(res, errStr, duration)
		if escalation != nil {
			l.broker.PublishMustDeliver(&EscalationEvent{
				SubAgentRole:   d.taskRole,
				SubAgentPrompt: d.taskPrompt,
				Severity:       string(escalation.Severity),
				Summary:        escalation.Summary,
				Detail:         escalation.Detail,
				Suggestion:     escalation.Suggestion,
			})
		}
		// Gates run even when an escalation was parsed: escalation and
		// gate verdict are recorded independently (review B8).
		if note := d.runQualityGates(ctx, res); note != "" {
			res += note
		}
	}

	l.Hooks.Emit(HookEvent{
		Event:      events.ToolEnd,
		ToolName:   d.call.Function.Name,
		ToolArgs:   d.call.Function.Arguments,
		ToolResult: res,
		DurationMs: duration.Milliseconds(),
		ToolError:  errStr,
	})

	// Publish usage before the result: executeAndCollect proceeds as soon as
	// it receives this value, so a deferred fold would race the parent's next
	// turn (and its final usage flush).
	d.finishSubAgent()
	results <- toolExecResult{idx: d.idx, callID: d.call.ID, name: d.call.Function.Name, args: d.call.Function.Arguments, content: res, dur: duration, err: err, meta: supervisionMeta}
}

// acquire gates the call on the sub-agent semaphore (foreground tasks)
// and the tool concurrency middleware, blocking until both admit the
// call or ctx ends.
func (d *toolDispatch) acquire(ctx context.Context) error {
	l := d.loop
	if d.isDelegate && l.subAgentSem != nil {
		select {
		case l.subAgentSem <- struct{}{}:
			d.releaseSubAgent = func() { <-l.subAgentSem }
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if l.toolConcurrency != nil {
		observability.RecordToolGoroutine(ctx, d.call.Function.Name, "acquire_concurrency")
		if err := l.toolConcurrency.Acquire(ctx); err != nil {
			// Release the sub-agent slot already taken above — run()'s
			// deferred release is not registered yet when acquire fails
			// (review: semaphore leak).
			d.release()
			return err
		}
		d.releaseTool = l.toolConcurrency.Release
	}
	return nil
}

// release frees the concurrency slots held by the dispatch. Idempotent
// so the acquire-failure path and the deferred cleanup can both call it.
func (d *toolDispatch) release() {
	if d.releaseTool != nil {
		d.releaseTool()
		d.releaseTool = nil
	}
	if d.releaseSubAgent != nil {
		d.releaseSubAgent()
		d.releaseSubAgent = nil
	}
}

// publishStartFn returns the closure that publishes this dispatch's
// SubAgentStartEvent once the child's model is known.
func (d *toolDispatch) publishStartFn() func(model string) {
	l := d.loop
	return func(model string) {
		if l.broker != nil {
			l.broker.PublishMustDeliver(&SubAgentStartEvent{
				SubAgentID:    d.subAgentID,
				Role:          d.taskRole,
				Model:         model,
				Prompt:        d.taskPrompt,
				World:         d.world.Resolved,
				WorldDegraded: d.world.Degraded,
				WorldReason:   d.world.Reason,
			})
		}
	}
}

// watchdogState is the liveness evidence a kill verdict depends on: whether
// the dispatch world became ready, how many heartbeats arrived afterwards, and
// which world was stood up. The watchdog goroutine writes it and the dispatch
// goroutine reads it, so every field is guarded.
type watchdogState struct {
	mu      sync.Mutex
	ready   bool
	fired   bool
	beats   int
	world   tools.SubAgentWorld
	timeout time.Duration
}

// markReady records that the dispatch world exists and the child's run is
// about to begin, capturing the world so a startup verdict can name what was
// stood up when the child never beat.
func (w *watchdogState) markReady(world tools.SubAgentWorld) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ready = true
	w.world = world
}

// beat records one liveness heartbeat from the child.
func (w *watchdogState) beat() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.beats++
}

// markFired records that the deadline elapsed and the watchdog cancelled the
// dispatch.
func (w *watchdogState) markFired() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.fired = true
}

// snapshot returns the recorded state. Callers must hold no lock.
func (w *watchdogState) snapshot() (ready, fired bool, beats int, world tools.SubAgentWorld, timeout time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ready, w.fired, w.beats, w.world, w.timeout
}

// setupSubAgent wires model/usage capture, the start notifier, and the
// stuck-child watchdog for a foreground sub-agent dispatch, returning
// the context the child should run under.
func (d *toolDispatch) setupSubAgent(runCtx context.Context) context.Context {
	l := d.loop
	publishStart := d.publishStartFn()

	runCtx = tools.WithSubAgentModelPtr(runCtx, &d.subAgentModel)
	runCtx = tools.WithSubAgentWorldPtr(runCtx, &d.world)
	runCtx = tools.WithSubAgentStartNotifier(runCtx, func(model string) {
		d.startOnce.Do(func() { publishStart(model) })
	})
	runCtx = tools.WithSubAgentUsage(runCtx, &d.subUsage)

	childTimeout := l.Config.StuckChildTimeout
	if l.Config.StuckChildTimeouts != nil {
		if t, ok := l.Config.StuckChildTimeouts[d.taskRole]; ok && t > 0 {
			childTimeout = t
		}
	}
	if childTimeout > 0 {
		d.watchdogActive = true
		d.watch.mu.Lock()
		d.watch.timeout = childTimeout
		d.watch.mu.Unlock()

		hb := make(chan struct{}, 1)
		ready := make(chan struct{})
		d.watchDone = make(chan struct{})
		runCtx = tools.WithSubAgentHeartbeat(runCtx, hb)
		// The dispatch body reports its world ready once provisioning is
		// done; that — not arming — is when the stuck-child deadline starts
		// counting (yaah-95ri.2).
		var readyOnce sync.Once
		runCtx = tools.WithSubAgentReadyNotifier(runCtx, func() {
			readyOnce.Do(func() {
				d.watch.markReady(d.world)
				close(ready)
			})
		})
		watchCtx, watchCancel := context.WithCancel(runCtx)
		runCtx = watchCtx
		go d.armStuckChildWatchdog(watchCtx, watchCancel, ready, d.watchDone, hb, childTimeout)
	}
	return runCtx
}

// armStuckChildWatchdog supervises one child dispatch.
//
// It waits for the dispatch world to become ready before it starts the
// deadline at all: creating a worktree or starting a container cannot
// heartbeat, so counting that window against the child killed healthy
// dispatches as "stuck" (yaah-95ri.2). Only after readiness — the point at
// which the child's loop is expected to beat — does the deadline run, and it
// is reset by every heartbeat.
//
// When the deadline elapses the kill is classified from the evidence
// collected in d.watch: a child that never beat after its world was ready
// never started (ErrDispatchStartup), while a child that beat and then went
// silent is stuck (ErrStuckChild) (yaah-95ri.3).
func (d *toolDispatch) armStuckChildWatchdog(ctx context.Context, cancel context.CancelFunc, ready <-chan struct{}, done <-chan struct{}, hb <-chan struct{}, timeout time.Duration) {
	defer cancel()
	select {
	case <-ready:
	case <-done:
		return
	case <-ctx.Done():
		return
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-hb:
			d.watch.beat()
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(timeout)
		case <-timer.C:
			// The select may pick the timer over a heartbeat that is
			// already buffered; give a pending beat precedence so an
			// in-flight heartbeat is not read as zero heartbeats (which
			// would misreport a mid-run stall as a dispatch-startup
			// failure, yaah-95ri.3).
			select {
			case <-hb:
				d.watch.beat()
				timer.Reset(timeout)
				continue
			default:
			}
			d.watch.markFired()
			cancel()
			return
		case <-done:
			return
		case <-ctx.Done():
			return
		}
	}
}

// killVerdict classifies a cancelled sub-agent dispatch.
//
// ErrDispatchStartup is the verdict only when the watchdog itself fired and
// the child had not produced a single heartbeat since its world became ready:
// the dispatch never started, so the world/provisioning phase is what failed,
// not the child's run. A child that beat and then stopped mid-run is stuck,
// and a cancellation the watchdog did not cause keeps the historical stuck
// classification (yaah-95ri.3).
func (d *toolDispatch) killVerdict() error {
	_, fired, beats, world, timeout := d.watch.snapshot()
	if !fired || beats > 0 {
		return tools.ErrStuckChild
	}
	detail := fmt.Sprintf("no heartbeat within %s of world readiness", timeout)
	if world.Resolved != "" {
		detail += fmt.Sprintf(" (world: %s)", world.Resolved)
	}
	if world.Degraded {
		if world.Reason != "" {
			detail += fmt.Sprintf(" (degraded: %s)", world.Reason)
		} else {
			detail += " (degraded)"
		}
	}
	return fmt.Errorf("%w — %s", tools.ErrDispatchStartup, detail)
}

// finishSubAgent delivers usage captured from the child back to the
// loop. Called by owners of a foreground dispatch once execution ends.
func (d *toolDispatch) finishSubAgent() {
	d.finishOnce.Do(func() {
		if d.watchDone != nil {
			close(d.watchDone)
			d.watchDone = nil
		}
		if d.subUsage.TotalTokens > 0 {
			d.loop.addSubAgentUsage(d.subUsage)
			if d.loop.OnSubUsage != nil {
				d.loop.OnSubUsage(d.subUsage)
			}
		}
	})
}

// emitSubAgentEnd publishes the SubAgentEndEvent for a foreground
// dispatch.
func (d *toolDispatch) emitSubAgentEnd(res, errStr string, duration time.Duration) {
	l := d.loop
	model := d.subAgentModel
	if model == "" {
		model = l.Config.Model
	}
	endEvt := &SubAgentEndEvent{
		SubAgentID:    d.subAgentID,
		Role:          d.taskRole,
		Model:         model,
		Prompt:        d.taskPrompt,
		Duration:      duration,
		Error:         errStr,
		World:         d.world.Resolved,
		WorldDegraded: d.world.Degraded,
		WorldReason:   d.world.Reason,
	}
	if errStr == "" {
		endEvt.Result = res
	}
	l.broker.PublishMustDeliver(endEvt)
}

// runQualityGates dispatches each configured validator for the role and
// returns the note to append to the sub-agent result when a gate fails.
// Unlike the previous bare Registry.Execute, gates run through the same
// supervised dispatch path as foreground sub-agents — sub-agent
// semaphore, model/usage capture, per-role watchdog, SubAgentStart/End
// events, and an OTel sub-agent span (review B8).
func (d *toolDispatch) runQualityGates(ctx context.Context, result string) string {
	l := d.loop
	var note strings.Builder
	for _, validatorRole := range l.Config.QualityGates[d.taskRole] {
		gatePrompt := fmt.Sprintf(
			"Validate the following sub-agent output. Run relevant tests, "+
				"check for errors, and report PASS or FAIL with details.\n\n"+
				"## Sub-agent output (role: %s)\n\n%.8000s",
			d.taskRole, result,
		)
		gateRes, gateErr := l.runGateDispatch(ctx, validatorRole, gatePrompt)
		if gateErr != nil {
			// A dispatch error is infrastructure, not a verdict; the
			// original behavior skips the gate rather than failing the
			// parent on it.
			slog.Debug("quality gate dispatch failed", "validator", validatorRole, "err", gateErr)
			continue
		}
		if gateVerdictFailed(validatorRole, gateRes) {
			if len(gateRes) > 2000 {
				gateRes = gateRes[:2000] + "\n...[truncated]"
			}
			note.WriteString("\n\n[quality-gate:FAIL] Validator " + validatorRole + " found issues:\n" + gateRes)
		}
	}
	return note.String()
}

// acquireGateSlot takes a sub-agent slot for a quality-gate dispatch if one
// is free. It must never block: the parent sub-agent dispatch already holds
// a slot that accounts for this work, so a blocking acquire would deadlock
// at MaxSubAgentConcurrency == 1.
//
// The price of that non-blocking acquire is that a validator can run
// unslotted — outside the configured cap — and oversubscribe a slow
// provider. The trade itself stays (deadlock avoidance is worth more than a
// transient overrun), but it is no longer invisible: each unslotted dispatch
// is counted on the loop and reported at warn level with the running total,
// so the orchestrator can see the cap being exceeded (yaah-eyh).
func (d *toolDispatch) acquireGateSlot(validatorRole string) {
	l := d.loop
	if !d.isDelegate || l.subAgentSem == nil {
		return
	}
	select {
	case l.subAgentSem <- struct{}{}:
		d.releaseSubAgent = func() { <-l.subAgentSem }
	default:
		total := l.unslottedGates.Add(1)
		slog.Warn("quality gate dispatch ran without a sub-agent slot — validator work can exceed the configured concurrency cap (kept to avoid deadlock at max_concurrency=1)",
			"validator", validatorRole,
			"max_concurrency", cap(l.subAgentSem),
			"unslotted_total", total,
		)
	}
}

// UnslottedGateDispatches reports how many quality-gate validator dispatches
// this loop has run without taking a sub-agent slot — the oversubscription
// the non-blocking gate acquire trades for deadlock avoidance (yaah-eyh).
// Reporters that summarise a run can include it; zero means every gate ran
// inside the configured cap.
func (l *Loop) UnslottedGateDispatches() int64 { return l.unslottedGates.Load() }

// runGateDispatch executes one validator sub-agent through the
// supervised dispatch path. It publishes no ToolStart/ToolEnd events:
// the gate is not a conversation tool call; only SubAgentStart/End
// events reach views.
func (l *Loop) runGateDispatch(ctx context.Context, validatorRole, gatePrompt string) (string, error) {
	gateArgs := fmt.Sprintf(`{"prompt":%q,"role":%q,"description":"quality gate: %s"}`,
		gatePrompt, validatorRole, validatorRole)
	gd := l.newDispatch(-1, types.ToolCall{
		ID:   fmt.Sprintf("gate-%s", validatorRole),
		Type: "function",
		Function: types.ToolCallFn{
			Name:      "delegate",
			Arguments: gateArgs,
		},
	})

	// Non-blocking semaphore acquire: the parent sub-agent dispatch
	// already holds a slot that accounts for this work, and a blocking
	// acquire could deadlock when MaxSubAgentConcurrency == 1. The
	// consequence — an unslotted validator run — is counted and reported
	// rather than silently ignored (yaah-eyh).
	gd.acquireGateSlot(validatorRole)
	if l.toolConcurrency != nil {
		if err := l.toolConcurrency.Acquire(ctx); err != nil {
			gd.release()
			return "", err
		}
		gd.releaseTool = l.toolConcurrency.Release
	}
	defer gd.release()
	defer gd.finishSubAgent()

	runCtx := ctx
	var span trace.Span
	if l.Config.OtelEnabled {
		runCtx, span = observability.StartSubAgent(ctx, validatorRole, gatePrompt)
	}
	runCtx = gd.setupSubAgent(runCtx)

	start := time.Now()
	res, err := l.Registry.Execute(runCtx, "delegate", gateArgs)

	// Fallback start announce for runners that never notified.
	gd.startOnce.Do(func() {
		model := gd.subAgentModel
		if model == "" {
			model = l.Config.Model
		}
		gd.publishStartFn()(model)
	})

	if gd.watchdogActive && ctx.Err() == nil && runCtx.Err() == context.Canceled {
		// Gate callers only log dispatch errors; wrap the structured
		// result so the child's partial output reaches that log instead
		// of being dropped (yaah-1ep). The verdict separates a stuck child
		// from a dispatch that never started (yaah-95ri.3).
		verdict := gd.killVerdict()
		if res != "" {
			err = fmt.Errorf("%w: %s", verdict, l.truncateToolResult(res))
		} else {
			err = verdict
		}
		res = ""
	}
	if l.Config.OtelEnabled && span != nil {
		observability.FinishSubAgent(span, err)
		span.End()
	}
	duration := time.Since(start)

	errStr := ""
	if err != nil {
		errStr = err.Error()
	} else {
		res = l.truncateToolResult(res)
	}
	if l.broker != nil {
		gd.emitSubAgentEnd(res, errStr, duration)
	}
	if err != nil {
		return "", err
	}
	return res, nil
}
