package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SubAgentParams carries the per-invocation sub-agent configuration that
// the model may supply via the task tool arguments. It is passed through
// to the TaskRunner so the runner can build a role-appropriate Loop.
type SubAgentParams struct {
	// Role selects the tool profile and default limits. Empty means the
	// legacy default tool set.
	Role string

	// TimeoutSeconds, when > 0, overrides the role's default timeout.
	TimeoutSeconds int

	// MaxIterations, when > 0, overrides the role's default iteration cap.
	MaxIterations int
}

// TaskRunner runs a sub-agent for a given prompt and role configuration
// and returns its final response. The runner is responsible for building
// the role-specific tool registry and Loop.
type TaskRunner func(ctx context.Context, prompt string, params SubAgentParams) (string, error)

// TaskTool spawns a sub-agent with a restricted tool set to complete a task.
//
// The tool supports an optional role (worker/reviewer/planner) that
// selects the sub-agent's tool set, iteration budget, and default
// timeout. Optional timeout_seconds and max_iterations override the
// role's defaults. When a sub-agent times out or is cancelled, the tool
// returns a structured JSON result rather than a hard error so the
// parent agent can decide whether to retry or move on.
type TaskTool struct {
	Runner TaskRunner

	// ResolveTimeout returns the effective default deadline for a call
	// when the model did not supply timeout_seconds. It receives the
	// parsed params so it can vary by role. May be nil (no default
	// timeout). The caller is expected to fold in role/profile/config
	// defaults; TaskTool clamps any per-call override to the schema
	// bounds before this is consulted.
	ResolveTimeout func(params SubAgentParams) time.Duration

	// RoleNames is the active set of sub-agent role names known to the
	// registry. When non-empty the schema's role enum is built
	// dynamically so user-defined roles are visible to the model.
	// When empty the legacy static schema is used.
	RoleNames []string
}

func (t *TaskTool) Name() string { return "task" }
func (t *TaskTool) Description() string {
	return "Launches a sub-agent with restricted tools to research or complete a subtask."
}

func (t *TaskTool) Schema() json.RawMessage {
	if len(t.RoleNames) > 0 {
		return BuildTaskSchema(t.RoleNames)
	}
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"description": {"type": "string", "description": "3-5 word description of the subtask"},
			"prompt": {"type": "string", "description": "The task for the sub-agent to perform autonomously"},
			"role": {"type": "string", "enum": ["worker", "reviewer", "planner"], "description": "Sub-agent role selecting its tool set and limits. worker = code changes and shell access; reviewer = read-only analysis; planner = can spawn workers via task. Omit for the legacy default tool set."},
			"timeout_seconds": {"type": "integer", "minimum": 10, "maximum": 600, "description": "Optional wall-clock deadline for the sub-agent. Overrides the role default."},
			"max_iterations": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Optional cap on sub-agent loop turns. Overrides the role default."}
		},
		"required": ["description", "prompt"]
	}`)
}

// BuildTaskSchema returns a JSON Schema for the task tool whose role
// enum is populated from the given list of role names, so user-defined
// roles discovered at runtime are visible to the model.
func BuildTaskSchema(roleNames []string) json.RawMessage {
	roles := make([]string, len(roleNames))
	copy(roles, roleNames)

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"description": map[string]any{
				"type":        "string",
				"description": "3-5 word description of the subtask",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "The task for the sub-agent to perform autonomously",
			},
			"role": map[string]any{
				"type":        "string",
				"enum":        roles,
				"description": "Sub-agent role selecting its tool set and limits. Omit for the legacy default tool set.",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"minimum":     10,
				"maximum":     600,
				"description": "Optional wall-clock deadline for the sub-agent. Overrides the role default.",
			},
			"max_iterations": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     50,
				"description": "Optional cap on sub-agent loop turns. Overrides the role default.",
			},
		},
		"required": []string{"description", "prompt"},
	}
	data, _ := json.Marshal(schema)
	return json.RawMessage(data)
}

func (t *TaskTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Description    string `json:"description"`
		Prompt         string `json:"prompt"`
		Role           string `json:"role"`
		TimeoutSeconds int    `json:"timeout_seconds"`
		MaxIterations  int    `json:"max_iterations"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("task: invalid arguments: %w", err)
	}
	if params.Prompt == "" {
		return "", fmt.Errorf("task: prompt is required")
	}

	if t.Runner == nil {
		return "", fmt.Errorf("task: sub-agent runner not configured")
	}

	// Clamp model-supplied overrides to the advertised schema bounds so a
	// runaway or injected value cannot neutralize the deadline or iterate
	// forever. Values below the minimum fall back to the role default.
	clampedTimeout := clampTimeoutSeconds(params.TimeoutSeconds)
	clampedIter := clampMaxIterations(params.MaxIterations)

	subParams := SubAgentParams{
		Role:           params.Role,
		TimeoutSeconds: clampedTimeout,
		MaxIterations:  clampedIter,
	}

	timeout := resolveTaskTimeout(clampedTimeout, t.ResolveTimeout, subParams)

	runCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	result, err := t.Runner(runCtx, params.Prompt, subParams)

	if err != nil {
		// A non-empty result alongside an error is treated as the
		// partial output the sub-agent produced before stopping.
		partial := result
		switch {
		case errors.Is(err, context.DeadlineExceeded), runCtx.Err() == context.DeadlineExceeded:
			return structuredTaskResult("timed out", timeout, partial), nil
		case errors.Is(err, context.Canceled), runCtx.Err() == context.Canceled:
			return structuredTaskResult("cancelled", timeout, partial), nil
		default:
			return "", err
		}
	}

	return result, nil
}

// clampTimeoutSeconds caps the model-supplied timeout at the schema's
// 600s maximum so a runaway or injected value cannot pin a sub-agent
// open indefinitely. Any positive value below the maximum is honoured
// as-is (the advertised minimum of 10s is advisory); 0 means "use the
// role default" and is preserved.
func clampTimeoutSeconds(v int) int {
	if v > 600 {
		return 600
	}
	if v < 0 {
		return 0
	}
	return v
}

// clampMaxIterations enforces the schema's 1–50 window. Values above 50
// are capped; 0 is preserved (means "use the role default").
func clampMaxIterations(v int) int {
	if v > 50 {
		return 50
	}
	if v < 0 {
		return 0
	}
	return v
}

// resolveTaskTimeout picks the effective timeout. A clamped per-call
// override wins; otherwise the role-aware resolver (if configured) is
// consulted. 0 means no timeout.
func resolveTaskTimeout(callSeconds int, resolveDefault func(SubAgentParams) time.Duration, params SubAgentParams) time.Duration {
	if callSeconds > 0 {
		return time.Duration(callSeconds) * time.Second
	}
	if resolveDefault != nil {
		return resolveDefault(params)
	}
	return 0
}

// structuredTaskResult builds the JSON payload returned to the parent
// agent when a sub-agent did not complete normally. Returning this as a
// successful tool result (rather than a Go error) lets the model decide
// whether to retry, continue, or report.
func structuredTaskResult(reason string, timeout time.Duration, partial string) string {
	timeoutStr := ""
	if timeout > 0 {
		timeoutStr = fmt.Sprintf("%.0fs", timeout.Seconds())
	}
	out := struct {
		Error   string `json:"error"`
		Timeout string `json:"timeout,omitempty"`
		Partial string `json:"partial,omitempty"`
	}{
		Error:   reason,
		Timeout: timeoutStr,
		Partial: partial,
	}
	data, err := json.Marshal(out)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, reason)
	}
	return string(data)
}
