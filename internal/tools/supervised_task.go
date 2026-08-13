package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/prompts"
)

// SupervisedTaskTool runs a sub-agent with checkpoint/rollback/retry.
//
// Unlike spawn_subagent, this tool is BLOCKING — the orchestrator's loop
// is stuck inside Execute until the sub-agent completes (or all retries
// are exhausted). This guarantees sequential execution: the orchestrator
// cannot dispatch a second supervised task while the first is running,
// preventing filesystem contention by construction.
//
// The tool creates a workspace checkpoint (via shepherd-kernel-go's
// git-based checkpoint) before running the sub-agent. If the sub-agent
// fails, the workspace is rolled back and the sub-agent is retried with
// guidance derived from the failure.
type SupervisedTaskTool struct {
	// Runner is the sub-agent execution closure (same type as TaskTool).
	Runner jobs.TaskRunner

	// ResolveTimeout returns the default timeout for a call when the
	// model did not supply timeout_seconds. Same contract as TaskTool.
	ResolveTimeout func(params jobs.SubAgentParams) time.Duration

	// RoleNames / RoleResolver / RoleDescriptions — same purpose as
	// TaskTool. The supervised tool exposes the same role selection.
	RoleNames        []string
	RoleResolver     func() []string
	RoleDescriptions map[string]string

	// RepoPath is the git repo for workspace checkpoints. If empty,
	// defaults to the current working directory at Execute time.
	RepoPath string

	// MaxRetries caps the rollback-and-retry cycles after the initial
	// attempt. 0 means one shot (no retry). Negative values fall back
	// to the default of 1.
	MaxRetries int
}

func (*SupervisedTaskTool) Name() string { return "supervised_task" }

func (*SupervisedTaskTool) Description() string {
	return prompts.ToolDescription("supervised_task")
}

func (t *SupervisedTaskTool) Schema() json.RawMessage {
	known := t.roleNames()
	if len(known) > 0 {
		return BuildSupervisedTaskSchema(known, t.RoleDescriptions)
	}
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"description": {"type": "string", "description": "Short label for this task (shown in UI)"},
			"prompt": {"type": "string", "description": "The task for the sub-agent to accomplish"},
			"role": {"type": "string", "description": "Sub-agent role selecting its tool set and limits. Required. Use list_subagents to see available roles."},
			"timeout_seconds": {"type": "integer", "minimum": 10, "maximum": 600, "description": "Per-attempt timeout"},
			"max_iterations": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Cap on sub-agent loop turns"}
		},
		"required": ["prompt", "role"]
	}`)
}

func (t *SupervisedTaskTool) Execute(ctx context.Context, args string) (string, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return "", fmt.Errorf("supervised_task: invalid arguments: %w", err)
	}

	for _, f := range []string{"timeout_seconds", "max_iterations"} {
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
	}
	if err := json.Unmarshal(fixed, &params); err != nil {
		return "", fmt.Errorf("supervised_task: invalid arguments: %w", err)
	}
	if params.Prompt == "" {
		return "", fmt.Errorf("supervised_task: prompt is required")
	}

	known := t.roleNames()
	if params.Role == "" {
		return "", fmt.Errorf("supervised_task: role is required — pick one of: %s", strings.Join(known, ", "))
	}
	if len(known) > 0 && !slices.Contains(known, params.Role) {
		return "", fmt.Errorf("supervised_task: %w — valid roles: %s", RoleNotFoundError{Role: params.Role}, strings.Join(known, ", "))
	}

	if t.Runner == nil {
		return "", fmt.Errorf("supervised_task: sub-agent runner not configured")
	}

	mgr := SharedScopeManager
	if mgr == nil {
		return "", fmt.Errorf("supervised_task: shepherd tracing not enabled (set shepherd_trace_dir in config)")
	}

	repoPath := t.RepoPath
	if repoPath == "" {
		repoPath, _ = os.Getwd()
	}

	clampedTimeout := clampTimeoutSeconds(params.TimeoutSeconds)
	clampedIter := clampMaxLoopCycles(params.MaxLoopCycles)

	subParams := jobs.SubAgentParams{
		Role:           params.Role,
		TimeoutSeconds: clampedTimeout,
		MaxLoopCycles:  clampedIter,
	}

	timeout := resolveTaskTimeout(clampedTimeout, t.ResolveTimeout, subParams)

	scopeID := fmt.Sprintf("supervised:%s:%d", params.Role, time.Now().UnixNano())
	scope, err := mgr.Create(scopeID)
	if err != nil {
		return "", fmt.Errorf("supervised_task: create scope: %w", err)
	}
	defer mgr.PruneCheckpoints(scope.ID())

	cp, err := mgr.CreateCheckpoint(scope.ID(), repoPath, nil)
	if err != nil {
		return "", fmt.Errorf("supervised_task: checkpoint: %w", err)
	}

	maxRetries := t.MaxRetries
	if maxRetries < 0 {
		maxRetries = 1
	}

	currentPrompt := params.Prompt
	lastPartial := ""

	// Turn-level restores inside a sub-agent attempt are recorded into
	// restoreStats by the loop (via the context) and surfaced in the
	// envelope for diagnostics.
	var restoreStats jobs.TurnRestoreStats

	for attempt := 0; attempt <= maxRetries; attempt++ {
		runCtx := ctx
		var cancel context.CancelFunc
		if timeout > 0 {
			runCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		runCtx = jobs.WithTurnRestoreStats(runCtx, &restoreStats)
		result, runErr := t.Runner(runCtx, currentPrompt, subParams)
		if cancel != nil {
			cancel()
		}
		if strings.TrimSpace(result) != "" {
			lastPartial = result
		}

		if runErr == nil && strings.TrimSpace(result) != "" {
			if attempt > 0 {
				result += fmt.Sprintf(" (succeeded on attempt %d after rollback)", attempt+1)
			}
			return completedSupervisedResult(attempt+1, result, &restoreStats), nil
		}

		// The parent cancelled the whole tool call — rolling back and
		// retrying would fight the cancellation. Report and stop.
		if ctx.Err() != nil {
			return structuredSupervisedResult("cancelled", attempt+1, lastPartial, runErr, &restoreStats), nil
		}

		if attempt == maxRetries {
			return structuredSupervisedResult("failed", attempt+1, lastPartial, runErr, &restoreStats), nil
		}

		// Checkpoints are single-use: restore consumes the current one,
		// so a fresh checkpoint is taken before the next attempt.
		if _, restoreErr := mgr.RestoreCheckpoint(cp.ID); restoreErr != nil {
			return structuredSupervisedResult("rollback_failed", attempt+1, lastPartial, restoreErr, &restoreStats), nil
		}

		cp, err = mgr.CreateCheckpoint(scope.ID(), repoPath, nil)
		if err != nil {
			return structuredSupervisedResult("recheckpoint_failed", attempt+1, lastPartial, err, &restoreStats), nil
		}

		currentPrompt = buildRetryPrompt(params.Prompt, attempt, result, runErr)
	}

	return structuredSupervisedResult("exhausted", maxRetries+1, "", nil, &restoreStats), nil
}

// roleNames returns the known role names for validation and schema
// generation (same pattern as TaskTool).
func (t *SupervisedTaskTool) roleNames() []string {
	known := make(map[string]bool)
	for _, n := range t.RoleNames {
		known[n] = true
	}
	if t.RoleResolver != nil {
		for _, n := range t.RoleResolver() {
			known[n] = true
		}
	}
	names := make([]string, 0, len(known))
	for n := range known {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// buildRetryPrompt constructs a retry prompt that carries forward the
// failure context so the sub-agent doesn't repeat the same mistakes.
func buildRetryPrompt(originalPrompt string, attempt int, result string, runErr error) string {
	var sb strings.Builder
	sb.WriteString(originalPrompt)
	sb.WriteString(fmt.Sprintf("\n\n--- SUPERVISOR GUIDANCE (attempt %d failed, retrying) ---\n", attempt+1))

	if runErr != nil {
		sb.WriteString("Previous attempt failed with error:\n")
		sb.WriteString(runErr.Error())
		sb.WriteString("\n\n")
	}

	if trimmed := strings.TrimSpace(result); trimmed != "" {
		sb.WriteString("Previous attempt output (before rollback):\n")
		if len(trimmed) > 2000 {
			trimmed = trimmed[:2000] + "\n...[truncated]"
		}
		sb.WriteString(trimmed)
		sb.WriteString("\n\n")
	}

	sb.WriteString("Your previous changes have been rolled back. Take a different approach.\n")
	return sb.String()
}

// supervisedTaskResult is the uniform, always-JSON envelope returned for every
// outcome of supervised_task. A single shape lets the caller distinguish a
// successful run from a failed-and-rolled-back run at a glance without
// re-inspecting the working tree. Returned as a successful tool result (not a
// Go error) so the model can decide whether to retry, continue, or report.
//
// Restores/RestoredFrom are turn-level diagnostics: how many times the
// sub-agent loop rewound a failed turn via its turn checkpoints (0 when
// turn checkpointing is off).
type supervisedTaskResult struct {
	Status       string `json:"status"`
	Attempts     int    `json:"attempts"`
	Result       string `json:"result,omitempty"`
	Error        string `json:"error,omitempty"`
	Partial      string `json:"partial,omitempty"`
	Restores     int    `json:"restores,omitempty"`
	RestoredFrom string `json:"restored_from,omitempty"`
}

// restoreFields copies the turn-restore diagnostics into the envelope.
// Nil stats contribute nothing.
func restoreFields(stats *jobs.TurnRestoreStats) (int, string) {
	if stats == nil {
		return 0, ""
	}
	return stats.Restores, stats.RestoredFrom
}

// completedSupervisedResult builds the envelope for a successful run.
func completedSupervisedResult(attempts int, result string, stats *jobs.TurnRestoreStats) string {
	restores, restoredFrom := restoreFields(stats)
	return marshalSupervisedResult(supervisedTaskResult{
		Status:       "completed",
		Attempts:     attempts,
		Result:       result,
		Restores:     restores,
		RestoredFrom: restoredFrom,
	})
}

// structuredSupervisedResult builds the envelope for a failed/cancelled run.
func structuredSupervisedResult(status string, attempts int, partial string, runErr error, stats *jobs.TurnRestoreStats) string {
	errMsg := ""
	if runErr != nil {
		errMsg = runErr.Error()
	}
	restores, restoredFrom := restoreFields(stats)
	return marshalSupervisedResult(supervisedTaskResult{
		Status:       status,
		Attempts:     attempts,
		Error:        errMsg,
		Partial:      partial,
		Restores:     restores,
		RestoredFrom: restoredFrom,
	})
}

func marshalSupervisedResult(out supervisedTaskResult) string {
	data, err := json.Marshal(out)
	if err != nil {
		return fmt.Sprintf(`{"status":%q}`, out.Status)
	}
	return string(data)
}

// BuildSupervisedTaskSchema returns a JSON Schema for the supervised task
// tool whose role enum is populated from the given list of role names.
// Unlike the spawn_subagent schema there is no background/parallel option
// (the tool is blocking by design) and no json_mode/output_limit knobs
// (managed by the role profile).
func BuildSupervisedTaskSchema(roleNames []string, roleDescriptions map[string]string) json.RawMessage {
	roles := make([]string, len(roleNames))
	copy(roles, roleNames)

	roleDesc := "Sub-agent role selecting its tool set and limits."
	if len(roleDescriptions) > 0 {
		var b strings.Builder
		b.WriteString("Sub-agent role. Available:\n")
		for _, name := range roles {
			if d, ok := roleDescriptions[name]; ok && d != "" {
				fmt.Fprintf(&b, "- %s: %s\n", name, d)
			} else {
				fmt.Fprintf(&b, "- %s\n", name)
			}
		}
		b.WriteString("Required — use list_subagents for full details.")
		roleDesc = strings.TrimSpace(b.String())
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"description": map[string]any{
				"type":        "string",
				"description": "Short label for this task (shown in UI)",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "The task for the sub-agent to accomplish",
			},
			"role": map[string]any{
				"type":        "string",
				"enum":        roles,
				"description": roleDesc,
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"minimum":     10,
				"maximum":     600,
				"description": "Per-attempt timeout",
			},
			"max_iterations": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     50,
				"description": "Cap on sub-agent loop turns",
			},
		},
		"required": []string{"prompt", "role"},
	}
	data, _ := json.Marshal(schema)
	return json.RawMessage(data)
}
