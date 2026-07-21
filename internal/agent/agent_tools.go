package agent

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/observability"
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

			isTask := tc.Function.Name == "task"
			var taskRole, taskPrompt string
			if isTask && l.OnSubAgent != nil {
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
			if l.toolSem != nil {
				select {
				case l.toolSem <- struct{}{}:
					releaseTool = func() { <-l.toolSem }
				case <-ctx.Done():
					if releaseSubAgent != nil {
						releaseSubAgent()
					}
					execResults <- toolExecResult{idx: i, callID: tc.ID, name: tc.Function.Name, args: tc.Function.Arguments, content: "cancelled", err: ctx.Err()}
					return
				}
			}
			defer func() {
				if releaseTool != nil {
					releaseTool()
				}
				if releaseSubAgent != nil {
					releaseSubAgent()
				}
			}()

			if isTask && l.OnSubAgent != nil {
				l.OnSubAgent(SubAgentInfo{Role: taskRole, Prompt: taskPrompt})
			}

			if l.OnTool != nil {
				l.OnTool(ToolInfo{Name: tc.Function.Name, Args: abbreviated})
			}

			l.emitHook(HookEvent{
				Event:    ToolStart,
				ToolName: tc.Function.Name,
				ToolArgs: tc.Function.Arguments,
			})

			start := time.Now()

			runCtx := ctx
			var toolSpan trace.Span
			if l.OtelEnabled {
				if isTask {
					runCtx, toolSpan = observability.StartSubAgent(ctx, taskRole, taskPrompt)
				} else {
					runCtx, toolSpan = observability.StartTool(ctx, tc.Function.Name, tc.Function.Arguments)
				}
			}

			res, err := l.Registry.Execute(runCtx, tc.Function.Name, tc.Function.Arguments)
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
			} else if len(res) > ToolResultMaxLen {
				res = res[:ToolResultMaxLen] + "\n...[truncated]..."
			}

			if isTask && l.OnSubAgent != nil {
				l.OnSubAgent(SubAgentInfo{Role: taskRole, Prompt: taskPrompt, Duration: duration, Error: errStr})
			}

			if l.OnTool != nil {
				info := ToolInfo{Name: tc.Function.Name, Args: abbreviated, Duration: duration, Result: res}
				if err != nil {
					info.Error = err.Error()
				}
				l.OnTool(info)
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
