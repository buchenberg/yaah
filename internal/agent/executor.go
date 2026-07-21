package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/types"
)

// maxInnerSummaryLen caps the inner executor loop's final summary to prevent
// unbounded context growth from the dual-loop architecture.
const maxInnerSummaryLen = 8000

// executorSystemPrompt is the purpose-built prompt for the inner executor.
// It is deliberately NOT the planner's identity prompt: the executor does
// tactical tool selection, not user-facing reasoning, so it gets only what
// its responsibility requires. This keeps its context small and stops it
// from narrating like a user-facing agent.
const executorSystemPrompt = `You are a tool executor. You receive a task directive, the user's original request, and the working directory. You run in the same filesystem as the planner — use absolute paths or paths relative to the working directory. Select and run the built-in tools needed to accomplish the directive; you may chain tools based on their results. When finished, respond with a terse structured summary: one line per tool executed naming the tool and its outcome (e.g. "write(path): wrote 138B", "bash(cmd): exit 0"). Do not write conversational prose, confirmations, or next-step plans. If a tool fails, you will see the error and can retry with a corrected approach — do not give up after one failure.`

// delegateToolName is the dispatch tool the planner may call to hand an
// intent-level directive to the executor. The planner keeps its full tool
// set and chooses per-action whether to delegate or work inline (the
// industry pattern: opencode task, crush agent). The executor owns tool
// selection for delegated work — one decision, one owner.
const delegateToolName = "delegate"

// splitDelegateCalls partitions a turn's tool calls into delegate calls
// (routed to the executor) and inline calls (executed directly). A turn may
// contain either, both, or neither.
func splitDelegateCalls(calls []types.ToolCall) (delegate, inline []types.ToolCall) {
	for _, c := range calls {
		if c.Function.Name == delegateToolName {
			delegate = append(delegate, c)
		} else {
			inline = append(inline, c)
		}
	}
	return
}

// parseDelegateCall extracts the directive and executor_type from a delegate
// call's JSON arguments. executor_type defaults to "default". Falls back to
// raw args if the model emitted something malformed, so a directive is always
// available.
func parseDelegateCall(args string) (directive, executorType string) {
	var v struct {
		Task         string `json:"task"`
		ExecutorType string `json:"executor_type"`
	}
	if err := json.Unmarshal([]byte(args), &v); err != nil || v.Task == "" {
		return strings.TrimSpace(args), "default"
	}
	if v.ExecutorType == "" {
		v.ExecutorType = "default"
	}
	return v.Task, v.ExecutorType
}

// lastUserMessage returns the content of the most recent user message in
// the conversation, used as the original intent handed to the executor.
func (l *Loop) lastUserMessage(msgs []types.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	return ""
}

// wrapExecutorResult wraps the executor's summary in a structured XML
// envelope so tool-return consumers (TUI, middleware, composition code) can
// programmatically detect state without parsing free-form prose. Mirrors
// opencode's <task id="..." state="..."> wrapping.
func wrapExecutorResult(summary string, exhausted bool, err error, truncated bool) string {
	state := "completed"
	if err != nil {
		state = "error"
	} else if exhausted {
		state = "exhausted"
	}
	return fmt.Sprintf(
		`<executor_result state="%s" truncated="%v">%s</executor_result>`,
		state, truncated, summary,
	)
}

// runExecutor runs the tool-execution loop that OWNS tool selection for a
// single delegated directive. It is the core of the executor-owns-tools
// architecture: the planner hands it an intent-level directive (not
// pre-formed tool calls), and the executor selects, runs, and chains tools
// to accomplish it.
//
// Key properties:
//   - It uses a purpose-built executorSystemPrompt (NOT the planner identity),
//     keeping its context small and stopping user-facing narration.
//   - It receives the ORIGINAL user intent alongside the directive, fixing
//     the "user is asking me to…" mischaracterization (the executor finally
//     sees the real request, not a reframed subtask).
//   - Tool selection happens here and ONLY here — the planner delegates
//     intent, the executor selects tools.
//
// Returns the executor's final summary and whether it exhausted its budget.
func (l *Loop) runExecutor(ctx context.Context, directive, originalIntent, executorType string) (string, bool, error) {
	var span trace.Span
	if l.OtelEnabled {
		ctx, span = observability.StartInnerLoop(ctx, directive)
		defer span.End()
	}

	provider, model := l.resolveExecutor(executorType)
	if span != nil {
		span.SetAttributes(
			attribute.String("inner.model", model),
			attribute.Bool("inner.dedicated_provider", l.ExecutorProvider != nil && l.ExecutorProvider != l.Provider),
			attribute.String("inner.executor_type", executorType),
		)
		if l.Model != "" {
			span.SetAttributes(attribute.String("outer.model", l.Model))
		}
	}

	// payload: directive + original intent + working directory.
	// The executor model gets a minimal system prompt and needs to know
	// WHERE it is running to resolve relative paths. Without this, the
	// executor returns results with wrong paths (e.g. /workspace/…),
	// the planner sees insufficient results, and re-runs tools inline —
	// producing an extra turn for every delegation.
	payload := directive
	if originalIntent != "" {
		payload += "\n\n## Original user request\n" + originalIntent
	}
	if wd, err := os.Getwd(); err == nil {
		payload += "\n\n## Working directory\n" + wd
	}
	messages := []types.Message{
		types.SystemMsg(executorSystemPrompt),
		types.UserMsg(payload),
	}
	if l.OtelVerbose && span != nil {
		observability.RecordInnerTask(span, payload)
		observability.RecordConversation(span, messages)
	}
	executorTools := l.buildExecutorToolDefs()

	// Snapshot total tokens before executor loop so we can attribute the
	// executor's usage separately from the planner on the inner.loop span.
	tokensBefore := l.TotalTokens

	for iter := 0; iter < l.MaxInnerIterations; iter++ {
		req := types.ChatRequest{Model: model, Messages: messages, Tools: executorTools}
		msg, err := l.getExecutorMessage(ctx, provider, req)
		if err != nil {
			if span != nil {
				observability.FinishInnerLoop(span, iter+1, false, err)
			}
			return "", false, fmt.Errorf("executor: %w", err)
		}
		messages = append(messages, msg)
		if l.OtelVerbose && span != nil {
			observability.RecordAssistantResponse(span, msg, "")
		}

		// No tool calls — executor is done, return the summary text.
		if len(msg.ToolCalls) == 0 {
			if span != nil {
				tokensAfter := l.TotalTokens
				span.SetAttributes(
					attribute.Int("inner.prompt_tokens", tokensAfter.PromptTokens-tokensBefore.PromptTokens),
					attribute.Int("inner.completion_tokens", tokensAfter.CompletionTokens-tokensBefore.CompletionTokens),
					attribute.Int("inner.iterations", iter+1),
				)
				observability.FinishInnerLoop(span, iter+1, false, nil)
			}
			return msg.Content, false, nil
		}

		// Execute the executor-selected tools and feed results back so it
		// can chain based on outcomes.
		for _, tc := range msg.ToolCalls {
			abbr := abbreviateArgs(tc.Function.Arguments, 60)
			if l.OnTool != nil {
				l.OnTool(ToolInfo{Name: tc.Function.Name, Args: abbr})
			}
			result := l.executeOneTool(ctx, tc)
			if l.OnTool != nil {
				errMsg := ""
				if result.err != nil {
					errMsg = result.err.Error()
				}
				l.OnTool(ToolInfo{Name: tc.Function.Name, Args: abbr, Duration: result.dur, Result: result.content, Error: errMsg})
			}
			messages = append(messages, types.Message{
				Role:       "tool",
				Content:    result.content,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
			})
			if result.err != nil {
				// Feed the error back to the executor so it can self-correct
				// on the next iteration instead of failing to the planner.
				// The executor sees the error as a tool result and can try
				// a fixed command — no need for the planner to re-do inline.
				continue
			}
		}
	}

	if span != nil {
		tokensAfter := l.TotalTokens
		span.SetAttributes(
			attribute.Int("inner.prompt_tokens", tokensAfter.PromptTokens-tokensBefore.PromptTokens),
			attribute.Int("inner.completion_tokens", tokensAfter.CompletionTokens-tokensBefore.CompletionTokens),
			attribute.Int("inner.iterations", l.MaxInnerIterations),
		)
		observability.FinishInnerLoop(span, l.MaxInnerIterations, true, nil)
	}
	return "", true, fmt.Errorf("executor exhausted after %d iterations", l.MaxInnerIterations)
}

// getExecutorMessage obtains one assistant message from the executor
// provider, using streaming when available (reusing runStream so token
// accounting and callbacks stay consistent with the main loop).
func (l *Loop) getExecutorMessage(ctx context.Context, p Provider, req types.ChatRequest) (types.Message, error) {
	if sp, ok := p.(StreamProvider); ok {
		msg, err := l.runStream(ctx, sp, req)
		if err != nil {
			return types.Message{}, err
		}
		return msg, nil
	}
	resp, err := p.Send(ctx, req)
	if err != nil {
		return types.Message{}, err
	}
	if len(resp.Choices) == 0 {
		return types.Message{}, fmt.Errorf("executor: no choices in response")
	}
	return resp.Choices[0].Message, nil
}

// resolveExecutor selects the executor provider/model for a delegated task.
// v1: every executor_type resolves to the same executor — dedicated if
// configured, else the main provider/model (the "use the default model"
// directive). executor_type is recorded for observability and is the
// forward-compat seam for a named executor roster (v2).
func (l *Loop) resolveExecutor(executorType string) (Provider, string) {
	provider := l.ExecutorProvider
	if provider == nil {
		provider = l.Provider
	}
	model := l.ExecutorModel
	if model == "" {
		model = l.Model
	}
	return provider, model
}

// delegateToolDef returns the planner's dispatch tool. Its description tells
// the model WHEN to delegate (multi-step / isolatable work) vs. work inline
// (single cheap tool) — the planner chooses per-action. executor_type
// defaults to "default" and is the seam for a named roster (v2).
func delegateToolDef() types.ToolDef {
	return types.ToolDef{
		Type: "function",
		Function: types.ToolFn{
			Name:        delegateToolName,
			Description: "Delegate a tool-execution task to the executor for context isolation, model tiering, and auto-approval. The executor runs tools without approval prompts — use this when inline tools like bash would be denied or when you need batch work (wc -l, grep -c, find, etc.). Use for multi-step tool work or work whose raw output you don't need in your own context. For a single cheap call (one read/glob/ls) prefer doing it inline. Provide an intent-level directive describing what to accomplish, not which tools to call — the executor selects tools. DELEGATION IS ESPECIALLY VALUABLE FOR: running test suites and capturing results, searching across many files, making coordinated multi-file edits, and any task requiring more than 2 tools.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"task": {"type": "string", "description": "Intent-level directive: what to accomplish."},
					"executor_type": {"type": "string", "description": "Executor variant to use. Defaults to \"default\".", "default": "default"}
				},
				"required": ["task"]
			}`),
		},
	}
}

// buildPlannerToolDefs returns the planner's tool set: the full registry set
// PLUS delegate, always (additive). The planner chooses inline vs. delegate
// per-action. This is the industry pattern (opencode task, crush agent) and
// avoids the substitutive shape change of v1.
func (l *Loop) buildPlannerToolDefs() []types.ToolDef {
	defs := l.buildToolDefs()
	defs = append(defs, delegateToolDef())
	return defs
}

// buildExecutorToolDefs returns the tool set the executor may use: the full
// registry set with NO delegate. Because delegate is constructed inline
// (never registered), buildToolDefs() cannot surface it — the executor
// structurally cannot delegate. This is the recursion guard, explicit by
// construction.
func (l *Loop) buildExecutorToolDefs() []types.ToolDef { return l.buildToolDefs() }
