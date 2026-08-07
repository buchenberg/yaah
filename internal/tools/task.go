package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/prompts"
)

// TaskTool spawns a sub-agent with a restricted tool set to complete a task.
//
// The tool supports an optional role (worker/reviewer/planner) that
// selects the sub-agent's tool set, iteration budget, and default
// timeout. Optional timeout_seconds and max_iterations override the
// role's defaults. When a sub-agent times out or is cancelled, the tool
// returns a structured JSON result rather than a hard error so the
// parent agent can decide whether to retry or move on.
//
// Background mode (background: true) dispatches the sub-agent in a
// goroutine and returns immediately. Results are delivered via
// BackgroundNotifier.
type TaskTool struct {
	Runner jobs.TaskRunner

	// ResolveTimeout returns the effective default deadline for a call
	// when the model did not supply timeout_seconds. It receives the
	// parsed params so it can vary by role. May be nil (no default
	// timeout). The caller is expected to fold in role/profile/config
	// defaults; TaskTool clamps any per-call override to the schema
	// bounds before this is consulted.
	ResolveTimeout func(params jobs.SubAgentParams) time.Duration

	// RoleNames is the active set of sub-agent role names known to the
	// registry. When non-empty the schema's role enum is built
	// dynamically so user-defined roles are visible to the model.
	// When empty the legacy static schema is used.
	RoleNames []string

	// RoleResolver, when non-nil, provides a live role-name lookup.
	// It is called at execution time to validate roles against the current
	// registry, so the spawn_subagent tool sees newly created roles immediately.
	RoleResolver func() []string

	// RoleDescriptions maps role name to a one-line description of what
	// the role does. Included in the spawn_subagent schema's role
	// parameter so the orchestrator can choose roles without calling
	// list_subagents. When empty no descriptions are appended.
	RoleDescriptions map[string]string

	// Tracker records file operations from sub-agent write/edit/delete
	// tools so the parent agent can detect when parallel workers touch
	// the same files. Nil means no tracking.
	Tracker *ConflictTracker

	// BackgroundJobs, when non-nil, manages asynchronous (background:
	// true) sub-agents: it owns the session-rooted context they derive
	// cancellation from (so they outlive the dispatching tool call and
	// turn), tracks them for status/cancel, attributes their usage, and
	// delivers their results as follow-ups. When nil, background mode is
	// unavailable and the tool errors if requested.
	BackgroundJobs *BackgroundJobs
}

func (t *TaskTool) Name() string { return "spawn_subagent" }
func (t *TaskTool) Description() string {
	return prompts.ToolDescription("task")
}

func (t *TaskTool) Execute(ctx context.Context, args string) (string, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return "", fmt.Errorf("spawn_subagent: invalid arguments: %w", err)
	}

	for _, f := range []string{"timeout_seconds", "max_iterations", "max_turns", "output_limit"} {
		if v, ok := raw[f]; ok {
			switch vv := v.(type) {
			case string:
				if n, err := coerceInt(vv); err == nil {
					raw[f] = n
				}
			case float64:
				raw[f] = int(vv)
			}
		}
	}

	fixed, _ := json.Marshal(raw)
	var params struct {
		Description    string `json:"description"`
		Prompt         string `json:"prompt"`
		Role           string `json:"role"`
		TimeoutSeconds int    `json:"timeout_seconds"`
		MaxLoopCycles  int    `json:"max_iterations"`
		MaxTurns       int    `json:"max_turns"`
		JSONMode       bool   `json:"json_mode"`
		OutputLimit    int    `json:"output_limit"`
		Background     bool   `json:"background"`
	}
	if err := json.Unmarshal(fixed, &params); err != nil {
		return "", fmt.Errorf("spawn_subagent: invalid arguments: %w", err)
	}
	if params.Prompt == "" {
		return "", fmt.Errorf("spawn_subagent: prompt is required")
	}

	// Role is required: there is no default role. Reject empty and
	// unknown roles here so the model gets a self-correcting tool
	// result instead of spawning an unconfigured sub-agent.
	known := t.roleNames()
	if params.Role == "" {
		return "", fmt.Errorf("spawn_subagent: role is required — pick one of: %s (use list_subagents for details)", strings.Join(known, ", "))
	}
	if len(known) > 0 && !slices.Contains(known, params.Role) {
		return "", fmt.Errorf("spawn_subagent: %w — valid roles: %s", RoleNotFoundError{Role: params.Role}, strings.Join(known, ", "))
	}

	if t.Runner == nil {
		return "", fmt.Errorf("spawn_subagent: sub-agent runner not configured")
	}

	// Clamp model-supplied overrides to the advertised schema bounds so a
	// runaway or injected value cannot neutralize the deadline or iterate
	// forever. Values below the minimum fall back to the role default.
	clampedTimeout := clampTimeoutSeconds(params.TimeoutSeconds)
	clampedIter := clampMaxLoopCycles(params.MaxLoopCycles)

	subParams := jobs.SubAgentParams{
		Role:           params.Role,
		TimeoutSeconds: clampedTimeout,
		MaxLoopCycles:  clampedIter,
		MaxToolTurns:   params.MaxTurns,
		JSONMode:       params.JSONMode,
		OutputLimit:    params.OutputLimit,
	}

	timeout := resolveTaskTimeout(clampedTimeout, t.ResolveTimeout, subParams)

	label := params.Role
	if params.Description != "" {
		label = label + " — " + params.Description
	}

	runCtx := WithConflictLabel(ctx, label)

	if params.Background {
		if t.BackgroundJobs == nil {
			return "", fmt.Errorf("spawn_subagent: background mode requested but not available")
		}
		jobID, err := t.BackgroundJobs.Launch(ctx, t.Runner, params.Role, params.Description, params.Prompt, subParams, timeout)
		if err != nil {
			return "", err
		}
		return structuredBackgroundResult(jobID, label), nil
	}

	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(runCtx, timeout)
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
		case errors.Is(err, jobs.ErrStuckChild):
			return structuredTaskResult("stuck", timeout, partial), nil
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

// clampMaxLoopCycles enforces the schema's 1–50 window. Values above 50
// are capped; 0 is preserved (means "use the role default").
func clampMaxLoopCycles(v int) int {
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
func resolveTaskTimeout(callSeconds int, resolveDefault func(jobs.SubAgentParams) time.Duration, params jobs.SubAgentParams) time.Duration {
	if callSeconds > 0 {
		return time.Duration(callSeconds) * time.Second
	}
	if resolveDefault != nil {
		return resolveDefault(params)
	}
	return 0
}

// structuredBackgroundResult builds a JSON payload for background sub-agent
// dispatch. The model sees this as the immediate tool result carrying a job
// id it can use with the subagent_jobs tool (status/cancel); the actual
// sub-agent output arrives later via a follow-up message.
func structuredBackgroundResult(jobID, label string) string {
	out := struct {
		Status string `json:"status"`
		JobID  string `json:"job_id"`
		Label  string `json:"label"`
	}{
		Status: "running",
		JobID:  jobID,
		Label:  label,
	}
	data, err := json.Marshal(out)
	if err != nil {
		return `{"status":"running"}`
	}
	return string(data)
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

func coerceInt(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}
