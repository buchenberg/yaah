package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/agent/events"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// ErrToolDenied is returned when a tool call is denied by approval policy
// or by the user.
var ErrToolDenied = errors.New("tool denied")

// executeAndCollect runs tool calls concurrently and returns ToolResult for middleware inspection.
func (l *Loop) executeAndCollect(ctx context.Context, calls []types.ToolCall, messages *[]types.Message) []pipeline.ToolResult {
	results := make([]pipeline.ToolResult, len(calls))
	ordered := make([]toolExecResult, len(calls))
	execResults := make(chan toolExecResult, len(calls))

	for i, tc := range calls {
		i, tc := i, tc

		if l.Config.ApprovalMode == "deny" && l.classifyDanger(tc.Function.Name, tc.Function.Arguments) {
			errMsg := fmt.Sprintf("error: tool %q requires approval but approval mode is 'deny'", tc.Function.Name)
			l.Hooks.Emit(HookEvent{Event: events.ToolStart, ToolName: tc.Function.Name, ToolArgs: tc.Function.Arguments})
			l.Hooks.Emit(HookEvent{Event: events.ToolEnd, ToolName: tc.Function.Name, ToolArgs: tc.Function.Arguments, ToolError: fmt.Sprintf("tool %q requires approval but approval mode is 'deny'", tc.Function.Name), ToolResult: errMsg})
			execResults <- toolExecResult{idx: i, callID: tc.ID, name: tc.Function.Name, args: tc.Function.Arguments, content: errMsg, err: ErrToolDenied}
			continue
		}
		if l.Config.ApprovalMode == "ask" && l.classifyDanger(tc.Function.Name, tc.Function.Arguments) {
			if !l.approveTool(tc.Function.Name, tc.Function.Arguments) {
				errMsg := fmt.Sprintf("error: tool %q was denied by user", tc.Function.Name)
				l.Hooks.Emit(HookEvent{Event: events.ToolStart, ToolName: tc.Function.Name, ToolArgs: tc.Function.Arguments})
				l.Hooks.Emit(HookEvent{Event: events.ToolEnd, ToolName: tc.Function.Name, ToolArgs: tc.Function.Arguments, ToolError: fmt.Sprintf("tool %q was denied by user", tc.Function.Name), ToolResult: errMsg})
				execResults <- toolExecResult{idx: i, callID: tc.ID, name: tc.Function.Name, args: tc.Function.Arguments, content: errMsg, err: ErrToolDenied}
				continue
			}
		}

		go func() {
			toolID := l.toolIDGen.Add(1)
			abbreviated := abbreviateArgs(tc.Function.Arguments, 80)

			isTask := tc.Function.Name == "spawn_subagent"
			var taskRole, taskPrompt string
			isBackground := false
			if isTask {
				taskRole, taskPrompt, isBackground = parseTaskArgs(tc.Function.Arguments)
			}

			var releaseSubAgent, releaseTool func()

			if isTask && l.subAgentSem != nil {
				select {
				case l.subAgentSem <- struct{}{}:
					releaseSubAgent = func() { <-l.subAgentSem }
				case <-ctx.Done():
					execResults <- toolExecResult{idx: i, callID: tc.ID, name: tc.Function.Name, args: tc.Function.Arguments, content: "cancelled", err: ctx.Err()}
					return
				}
			}
			if l.toolConcurrency != nil {
				if err := l.toolConcurrency.Acquire(ctx); err != nil {
					if releaseSubAgent != nil {
						releaseSubAgent()
					}
					execResults <- toolExecResult{idx: i, callID: tc.ID, name: tc.Function.Name, args: tc.Function.Arguments, content: "cancelled", err: err}
					return
				}
				releaseTool = l.toolConcurrency.Release
			}
			defer func() {
				if releaseTool != nil {
					releaseTool()
				}
				if releaseSubAgent != nil {
					releaseSubAgent()
				}
			}()

			if l.broker != nil {
				l.broker.PublishMustDeliver(&events.ToolStartEvent{ID: toolID, Name: tc.Function.Name, Args: abbreviated})
			}

			// The sub-agent start event is emitted by the runner (via the
			// start notifier below) once the child's model is resolved, so
			// views learn the role and model together. startOnce guards a
			// post-execution fallback for runners that never announce.
			// subAgentID identifies the sub-agent itself across its start,
			// end, and result events.
			var subAgentID string
			if isTask {
				subAgentID = fmt.Sprintf("sa-%d", l.subAgentIDGen.Add(1))
			}
			var startOnce sync.Once
			publishStart := func(model string) {
				if l.broker != nil {
					l.broker.PublishMustDeliver(&SubAgentStartEvent{SubAgentID: subAgentID, Role: taskRole, Model: model, Prompt: taskPrompt})
				}
			}

			l.Hooks.Emit(HookEvent{
				Event:    events.ToolStart,
				ToolName: tc.Function.Name,
				ToolArgs: tc.Function.Arguments,
			})

			start := time.Now()
			var subAgentModel string

			runCtx := ctx
			var toolSpan trace.Span
			if l.Config.OtelEnabled {
				if isTask && !isBackground {
					runCtx, toolSpan = observability.StartSubAgent(ctx, taskRole, taskPrompt)
				} else if isTask && isBackground {
					// A short span for the dispatch only; the background
					// job owns its own long-lived sub-agent span (created
					// by the BackgroundJobs manager as a child of this).
					runCtx, toolSpan = observability.StartTool(ctx, "spawn_subagent:dispatch", tc.Function.Arguments)
				} else {
					runCtx, toolSpan = observability.StartTool(ctx, tc.Function.Name, tc.Function.Arguments)
				}
			}

			var watchdogActive bool

			// Foreground sub-agents get model/usage capture, start/end
			// events, and the stuck-child watchdog wired here. Background
			// sub-agents skip all of this: the BackgroundJobs manager owns
			// their model/usage capture (via context pointers), emits
			// their start/end events through loop-registered callbacks,
			// and bounds them with their own timeout — none of which may
			// be tied to this per-call context (which is cancelled the
			// instant this goroutine returns).
			if isTask && !isBackground {
				runCtx = tools.WithSubAgentModelPtr(runCtx, &subAgentModel)
				runCtx = tools.WithSubAgentStartNotifier(runCtx, func(model string) {
					startOnce.Do(func() { publishStart(model) })
				})
				var subUsage types.Usage
				runCtx = tools.WithSubAgentUsage(runCtx, &subUsage)
				defer func() {
					if subUsage.TotalTokens > 0 {
						l.addUsage(subUsage)
					}
				}()

				childTimeout := l.Config.StuckChildTimeout
				if l.Config.StuckChildTimeouts != nil {
					if t, ok := l.Config.StuckChildTimeouts[taskRole]; ok && t > 0 {
						childTimeout = t
					}
				}
				if childTimeout > 0 {
					watchdogActive = true
					hb := make(chan struct{}, 1)
					runCtx = tools.WithSubAgentHeartbeat(runCtx, hb)
					watchCtx, watchCancel := context.WithCancel(runCtx)
					runCtx = watchCtx
					defer watchCancel()
					go func() {
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
			}

			res, err := l.Registry.Execute(runCtx, tc.Function.Name, tc.Function.Arguments)

			// Fallback for runners that never fired the start notifier:
			// emit the start event with the final model so views still see
			// the role/model pairing (arrives late but complete).
			// Background sub-agents are announced by the BackgroundJobs
			// manager, not here.
			if isTask && !isBackground {
				startOnce.Do(func() {
					model := subAgentModel
					if model == "" {
						model = l.Config.Model
					}
					publishStart(model)
				})
			}

			// Parse structured escalation from raw sub-agent output before
			// the result is truncated for display. Background dispatch
			// returns a {"status":"running"} placeholder with no payload
			// to parse.
			var escalation *tools.Escalation
			if isTask && !isBackground && l.broker != nil {
				output := tools.ParseSubAgentOutput(res, err)
				if output.Escalation != nil {
					escalation = output.Escalation
				}
			}

			if isTask && !isBackground && watchdogActive && ctx.Err() == nil && runCtx.Err() == context.Canceled {
				err = tools.ErrStuckChild
				res = ""
			}
			if l.Config.OtelEnabled && toolSpan != nil {
				if isTask && !isBackground {
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

			observability.RecordToolCall(runCtx, tc.Function.Name, duration, err != nil)

			if l.broker != nil {
				evt := &events.ToolEndEvent{
					ID:       toolID,
					Name:     tc.Function.Name,
					Args:     abbreviated,
					Result:   res,
					Duration: duration,
				}
				if err != nil {
					evt.Error = err.Error()
				}
				l.broker.PublishMustDeliver(evt)
			}

			if isTask && !isBackground && l.broker != nil {
				model := subAgentModel
				if model == "" {
					model = l.Config.Model
				}
				endEvt := &SubAgentEndEvent{
					SubAgentID: subAgentID,
					Role:       taskRole,
					Model:      model,
					Prompt:     taskPrompt,
					Duration:   duration,
					Error:      errStr,
				}
				if errStr == "" {
					endEvt.Result = res
				}
				l.broker.PublishMustDeliver(endEvt)
				if escalation != nil {
					l.broker.PublishMustDeliver(&EscalationEvent{
						SubAgentRole:   taskRole,
						SubAgentPrompt: taskPrompt,
						Severity:       string(escalation.Severity),
						Summary:        escalation.Summary,
						Detail:         escalation.Detail,
						Suggestion:     escalation.Suggestion,
					})
				}
				if escalation == nil && len(l.Config.QualityGates[taskRole]) > 0 {
					for _, validatorRole := range l.Config.QualityGates[taskRole] {
						gatePrompt := fmt.Sprintf(
							"Validate the following sub-agent output. Run relevant tests, "+
								"check for errors, and report PASS or FAIL with details.\n\n"+
								"## Sub-agent output (role: %s)\n\n%.8000s",
							taskRole, res,
						)
						gateArgs := fmt.Sprintf(`{"prompt":%q,"role":%q,"description":"quality gate: %s"}`,
							gatePrompt, validatorRole, taskRole)
						gateRes, gateErr := l.Registry.Execute(ctx, "spawn_subagent", gateArgs)
						if gateErr == nil && gateVerdictFail(gateRes) {
							if len(gateRes) > 2000 {
								gateRes = gateRes[:2000] + "\n...[truncated]"
							}
							res += "\n\n[quality-gate:FAIL] Validator " + validatorRole + " found issues:\n" + gateRes
						}
					}
				}
			}

			l.Hooks.Emit(HookEvent{
				Event:      events.ToolEnd,
				ToolName:   tc.Function.Name,
				ToolArgs:   tc.Function.Arguments,
				ToolResult: res,
				DurationMs: duration.Milliseconds(),
				ToolError:  errStr,
			})

			execResults <- toolExecResult{idx: i, callID: tc.ID, name: tc.Function.Name, args: tc.Function.Arguments, content: res, dur: duration, err: err}
		}()
	}

	for range len(calls) {
		r := <-execResults
		ordered[r.idx] = r
	}

	for i, r := range ordered {
		results[i] = pipeline.ToolResult{
			Name:     r.name,
			Args:     r.args,
			Result:   r.content,
			Error:    r.err,
			Duration: r.dur,
		}
		*messages = append(*messages, types.ToolResultMsg(r.callID, r.name, r.content))
		l.Persister.Persist((*messages)[len(*messages)-1])
	}

	return results
}

// gateVerdictFail determines whether a quality gate validator's output
// indicates failure. Uses last-occurrence heuristic: if "PASS" appears
// after the last "FAIL", the verdict is pass (avoids false positives from
// test output that mentions both words).
func gateVerdictFail(output string) bool {
	upper := strings.ToUpper(output)
	lastFail := strings.LastIndex(upper, "FAIL")
	if lastFail < 0 {
		return false
	}
	lastPass := strings.LastIndex(upper, "PASS")
	return lastPass < lastFail
}
