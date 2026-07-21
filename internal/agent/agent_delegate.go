package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/types"
)

// delegateToolName is the dispatch tool the planner may call to hand an
// intent-level directive to the executor.
const delegateToolName = "delegate"

// splitDelegateCalls partitions a turn's tool calls into delegate calls
// and inline calls.
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
// call's JSON arguments.
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

// lastUserMessage returns the content of the most recent user message.
func lastUserMessage(msgs []types.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	return ""
}

// wrapExecutorResult wraps the executor's summary in a structured XML envelope.
func wrapExecutorResult(summary string, exhausted bool, err error, truncated bool, fellBack bool) string {
	state := "completed"
	if err != nil {
		state = "error"
	} else if exhausted {
		state = "exhausted"
	}
	return fmt.Sprintf(
		`<executor_result state="%s" truncated="%v" fallback="%v">%s</executor_result>`,
		state, truncated, fellBack, summary,
	)
}

// delegateToolDef returns the planner's dispatch tool definition.
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
// PLUS delegate.
func (l *Loop) buildPlannerToolDefs() []types.ToolDef {
	defs := l.buildToolDefs()
	defs = append(defs, delegateToolDef())
	return defs
}
