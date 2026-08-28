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

	isTask       bool
	taskRole     string
	taskPrompt   string
	isBackground bool

	subAgentID    string
	subAgentModel string
	subUsage      types.Usage
	startOnce     sync.Once

	watchdogActive  bool
	releaseSubAgent func()
	releaseTool     func()
}

// newDispatch prepares the per-call state for one tool call.
func (l *Loop) newDispatch(idx int, call types.ToolCall) *toolDispatch {
	d := &toolDispatch{loop: l, idx: idx, call: call}
	d.toolID = l.toolIDGen.Add(1)
	d.abbreviated = abbreviateArgs(call.Function.Arguments, 80)
	d.isTask = call.Function.Name == "spawn_subagent"
	if d.isTask {
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
		l.broker.PublishMustDeliver(&events.ToolStartEvent{ID: d.toolID, Name: d.call.Function.Name, Args: d.abbreviated})
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

	runCtx := ctx
	var toolSpan trace.Span
	if l.Config.OtelEnabled {
		if d.isTask && !d.isBackground {
			runCtx, toolSpan = observability.StartSubAgent(ctx, d.taskRole, d.taskPrompt)
		} else if d.isTask && d.isBackground {
			// A short span for the dispatch only; the background job
			// owns its own long-lived sub-agent span (created by the
			// BackgroundJobs manager as a child of this).
			runCtx, toolSpan = observability.StartTool(ctx, "spawn_subagent:dispatch", d.call.Function.Arguments)
		} else {
			runCtx, toolSpan = observability.StartTool(ctx, d.call.Function.Name, d.call.Function.Arguments)
		}
	}

	if d.isTask && !d.isBackground {
		runCtx = d.setupSubAgent(runCtx)
	}

	res, err := l.Registry.Execute(runCtx, d.call.Function.Name, d.call.Function.Arguments)

	if d.isTask && !d.isBackground {
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
	if d.isTask && !d.isBackground && l.broker != nil {
		if output := tools.ParseSubAgentOutput(res, err); output.Escalation != nil {
			escalation = output.Escalation
		}
	}

	if d.isTask && !d.isBackground && d.watchdogActive && ctx.Err() == nil && runCtx.Err() == context.Canceled {
		err = tools.ErrStuckChild
		res = ""
	}
	if l.Config.OtelEnabled && toolSpan != nil {
		if d.isTask && !d.isBackground {
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
	} else {
		res = l.truncateToolResult(res)
	}

	observability.RecordToolCall(runCtx, d.call.Function.Name, duration, err != nil)

	if l.broker != nil {
		evt := &events.ToolEndEvent{
			ID:       d.toolID,
			Name:     d.call.Function.Name,
			Args:     d.abbreviated,
			Result:   res,
			Duration: duration,
		}
		if err != nil {
			evt.Error = err.Error()
		}
		l.broker.PublishMustDeliver(evt)
	}

	if d.isTask && !d.isBackground && l.broker != nil {
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

	results <- toolExecResult{idx: d.idx, callID: d.call.ID, name: d.call.Function.Name, args: d.call.Function.Arguments, content: res, dur: duration, err: err}
}

// acquire gates the call on the sub-agent semaphore (foreground tasks)
// and the tool concurrency middleware, blocking until both admit the
// call or ctx ends.
func (d *toolDispatch) acquire(ctx context.Context) error {
	l := d.loop
	if d.isTask && l.subAgentSem != nil {
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
			l.broker.PublishMustDeliver(&SubAgentStartEvent{SubAgentID: d.subAgentID, Role: d.taskRole, Model: model, Prompt: d.taskPrompt})
		}
	}
}

// setupSubAgent wires model/usage capture, the start notifier, and the
// stuck-child watchdog for a foreground sub-agent dispatch, returning
// the context the child should run under.
func (d *toolDispatch) setupSubAgent(runCtx context.Context) context.Context {
	l := d.loop
	publishStart := d.publishStartFn()

	runCtx = tools.WithSubAgentModelPtr(runCtx, &d.subAgentModel)
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
		hb := make(chan struct{}, 1)
		runCtx = tools.WithSubAgentHeartbeat(runCtx, hb)
		watchCtx, watchCancel := context.WithCancel(runCtx)
		runCtx = watchCtx
		go func() {
			defer watchCancel()
			timer := time.NewTimer(childTimeout)
			defer timer.Stop()
			for {
				select {
				case <-hb:
					if !timer.Stop() {
						<-timer.C
					}
					timer.Reset(childTimeout)
				case <-timer.C:
					watchCancel()
					return
				case <-watchCtx.Done():
					return
				}
			}
		}()
	}
	return runCtx
}

// finishSubAgent delivers usage captured from the child back to the
// loop. Called by owners of a foreground dispatch once execution ends.
func (d *toolDispatch) finishSubAgent() {
	if d.subUsage.TotalTokens > 0 {
		d.loop.addUsage(d.subUsage)
	}
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
		SubAgentID: d.subAgentID,
		Role:       d.taskRole,
		Model:      model,
		Prompt:     d.taskPrompt,
		Duration:   duration,
		Error:      errStr,
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
			Name:      "spawn_subagent",
			Arguments: gateArgs,
		},
	})

	// Non-blocking semaphore acquire: the parent sub-agent dispatch
	// already holds a slot that accounts for this work, and a blocking
	// acquire could deadlock when MaxSubAgentConcurrency == 1.
	if l.subAgentSem != nil {
		select {
		case l.subAgentSem <- struct{}{}:
			gd.releaseSubAgent = func() { <-l.subAgentSem }
		default:
		}
	}
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
	res, err := l.Registry.Execute(runCtx, "spawn_subagent", gateArgs)

	// Fallback start announce for runners that never notified.
	gd.startOnce.Do(func() {
		model := gd.subAgentModel
		if model == "" {
			model = l.Config.Model
		}
		gd.publishStartFn()(model)
	})

	if gd.watchdogActive && ctx.Err() == nil && runCtx.Err() == context.Canceled {
		err = tools.ErrStuckChild
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
