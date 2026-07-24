package agent

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// executeAndCollect runs tool calls concurrently and returns ToolResult for middleware inspection.
func (l *Loop) executeAndCollect(ctx context.Context, calls []types.ToolCall, messages *[]types.Message) []pipeline.ToolResult {
	results := make([]pipeline.ToolResult, len(calls))
	ordered := make([]toolExecResult, len(calls))
	execResults := make(chan toolExecResult, len(calls))

	for i, tc := range calls {
		i, tc := i, tc

		if l.ApprovalMode == "deny" && l.classifyDanger(tc.Function.Name, tc.Function.Arguments) {
			errMsg := fmt.Sprintf("error: tool %q requires approval but approval mode is 'deny'", tc.Function.Name)
			l.emitHook(HookEvent{Event: ToolStart, ToolName: tc.Function.Name, ToolArgs: tc.Function.Arguments})
			l.emitHook(HookEvent{Event: ToolEnd, ToolName: tc.Function.Name, ToolArgs: tc.Function.Arguments, ToolError: fmt.Sprintf("tool %q requires approval but approval mode is 'deny'", tc.Function.Name), ToolResult: errMsg})
			execResults <- toolExecResult{idx: i, callID: tc.ID, name: tc.Function.Name, args: tc.Function.Arguments, content: errMsg, err: fmt.Errorf("tool denied")}
			continue
		}
		if l.ApprovalMode == "ask" && l.classifyDanger(tc.Function.Name, tc.Function.Arguments) {
			if !l.approveTool(tc.Function.Name, tc.Function.Arguments) {
				errMsg := fmt.Sprintf("error: tool %q was denied by user", tc.Function.Name)
				l.emitHook(HookEvent{Event: ToolStart, ToolName: tc.Function.Name, ToolArgs: tc.Function.Arguments})
				l.emitHook(HookEvent{Event: ToolEnd, ToolName: tc.Function.Name, ToolArgs: tc.Function.Arguments, ToolError: fmt.Sprintf("tool %q was denied by user", tc.Function.Name), ToolResult: errMsg})
				execResults <- toolExecResult{idx: i, callID: tc.ID, name: tc.Function.Name, args: tc.Function.Arguments, content: errMsg, err: fmt.Errorf("tool denied")}
				continue
			}
		}

		go func() {
			abbreviated := abbreviateArgs(tc.Function.Arguments, 80)

			isTask := tc.Function.Name == "spawn_subagent"
			var taskRole, taskPrompt string
			if isTask {
				taskRole, taskPrompt = parseTaskArgs(tc.Function.Arguments)
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

			if isTask && l.broker != nil {
				l.broker.PublishMustDeliver(&SubAgentStartEvent{Role: taskRole, Prompt: taskPrompt})
			}
			if l.broker != nil {
				l.broker.PublishMustDeliver(&ToolStartEvent{Name: tc.Function.Name, Args: abbreviated})
			}

			l.emitHook(HookEvent{
				Event:    ToolStart,
				ToolName: tc.Function.Name,
				ToolArgs: tc.Function.Arguments,
			})

			start := time.Now()
			var subAgentModel string

			runCtx := ctx
			var toolSpan trace.Span
			if l.OtelEnabled {
				if isTask {
					runCtx, toolSpan = observability.StartSubAgent(ctx, taskRole, taskPrompt)
				} else {
					runCtx, toolSpan = observability.StartTool(ctx, tc.Function.Name, tc.Function.Arguments)
				}
			}

			var watchdogActive bool

			if isTask {
				runCtx = tools.WithSubAgentModelPtr(runCtx, &subAgentModel)
				var subUsage types.Usage
				runCtx = tools.WithSubAgentUsage(runCtx, &subUsage)
				defer func() {
					if subUsage.TotalTokens > 0 {
						l.addUsage(subUsage)
					}
				}()

				childTimeout := l.StuckChildTimeout
				if l.StuckChildTimeouts != nil {
					if t, ok := l.StuckChildTimeouts[taskRole]; ok && t > 0 {
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

			if isTask && watchdogActive && ctx.Err() == nil && runCtx.Err() == context.Canceled {
				err = tools.StuckChildError
				res = ""
			}
			if l.OtelEnabled && toolSpan != nil {
				if isTask {
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
				res = truncateToolResult(res)
			}

			if l.broker != nil {
				evt := &ToolEndEvent{
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

			if isTask && l.broker != nil {
				model := subAgentModel
				if model == "" {
					model = l.Model
				}
				l.broker.PublishMustDeliver(&SubAgentEndEvent{
					Role:     taskRole,
					Model:    model,
					Prompt:   taskPrompt,
					Duration: duration,
					Error:    errStr,
				})
			}

			l.emitHook(HookEvent{
				Event:      ToolEnd,
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
		l.persistMessage((*messages)[len(*messages)-1])
	}

	return results
}
