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

// DelegateTool spawns a sub-agent with a restricted tool set to complete a task.
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
type DelegateTool struct {
	Runner jobs.TaskRunner

	// ResolveTimeout returns the effective default deadline for a call
	// when the model did not supply timeout_seconds. It receives the
	// parsed params so it can vary by role. May be nil (no default
	// timeout). The caller is expected to fold in role/profile/config
	// defaults; DelegateTool clamps any per-call override to the schema
	// bounds before this is consulted.
	ResolveTimeout func(params jobs.SubAgentParams) time.Duration

	// RoleNames is the active set of sub-agent role names known to the
	// registry. When non-empty the schema's role enum is built
	// dynamically so user-defined roles are visible to the model.
	// When empty the legacy static schema is used.
	RoleNames []string

	// RoleResolver, when non-nil, provides a live role-name lookup.
	// It is called at execution time to validate roles against the current
	// registry, so the delegate tool sees newly created roles immediately.
	RoleResolver func() []string

	// LaneForRole, when non-nil, resolves the dispatch lane for a role at
	// execution time (yaah-7yf): proposal (isolated worktree + sealed
	// delta), report (isolated worktree, plain result, auto-discarded), or
	// shared (direct on the workspace). Nil routes every dispatch to the
	// proposal lane — the historical behavior.
	LaneForRole func(role string) DelegateLane

	// RoleDescriptions maps role name to a one-line description of what
	// the role does. Included in the delegate schema's role
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
	BackgroundJobs *jobs.BackgroundJobs

	// Retained, when non-nil, routes the dispatch through the retained-run
	// primitive (Plan 7 Phase 2): the sub-agent runs in an isolated worktree and
	// its delta is sealed into a proposal the orchestrator settles with the
	// proposal tool. Nil keeps the direct-to-host lane. Background dispatches are
	// retained too — their delivered result is a proposal envelope — so there is
	// no path from this tool to the host tree while Retained is set.
	Retained *RetainedRuntime
}

func (t *DelegateTool) Name() string { return "delegate" }
func (t *DelegateTool) Description() string {
	return prompts.ToolDescription("delegate")
}

func (t *DelegateTool) Execute(ctx context.Context, args string) (string, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return "", fmt.Errorf("delegate: invalid arguments: %w", err)
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
		return "", fmt.Errorf("delegate: invalid arguments: %w", err)
	}
	if params.Prompt == "" {
		return "", fmt.Errorf("delegate: prompt is required")
	}

	// Role is required: there is no default role. Reject empty and
	// unknown roles here so the model gets a self-correcting tool
	// result instead of spawning an unconfigured sub-agent.
	known := t.roleNames()
	if params.Role == "" {
		return "", fmt.Errorf("delegate: role is required — pick one of: %s (use list_subagents for details)", strings.Join(known, ", "))
	}
	if len(known) > 0 && !slices.Contains(known, params.Role) {
		return "", fmt.Errorf("delegate: %w — valid roles: %s", RoleNotFoundError{Role: params.Role}, strings.Join(known, ", "))
	}

	if t.Runner == nil {
		return "", fmt.Errorf("delegate: sub-agent runner not configured")
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
			return "", fmt.Errorf("delegate: background mode requested but not available")
		}
		// The retained lane covers background dispatches too: a background job
		// that ran the raw runner would mutate the host tree with no proposal,
		// which is exactly the guarantee "retained" makes. The job's delivered
		// result is then a proposal envelope like any other dispatch's — or,
		// for a report lane, the plain report with the worktree already torn
		// down. A shared-lane role reaches the raw runner directly: it cannot
		// mutate the tree, so the retained guarantee is moot.
		runner := t.Runner
		if t.Retained != nil && t.laneFor(params.Role) != LaneShared {
			rt := *t.Retained
			report := t.laneFor(params.Role) == LaneReport
			runner = func(ctx context.Context, prompt string, sp SubAgentParams) (string, error) {
				return startRetainedRun(ctx, rt, prompt, sp, timeout, report)
			}
		}
		jobID, err := t.BackgroundJobs.Launch(ctx, runner, params.Role, params.Description, params.Prompt, subParams, timeout)
		if err != nil {
			return "", err
		}
		return structuredBackgroundResult(jobID, label), nil
	}

	// Retained mode (Plan 7 Phase 2): run in an isolated worktree and return a
	// sealed proposal the orchestrator settles, instead of mutating the host.
	// The report lane (yaah-7yf) keeps the isolation but skips the sealing:
	// the result is the sub-agent's report text, and the worktree is torn
	// down automatically. A shared-lane role falls through to the direct
	// runner below — it cannot mutate the tree, so isolation buys nothing.
	if t.Retained != nil {
		switch t.laneFor(params.Role) {
		case LaneReport:
			return startRetainedRun(runCtx, *t.Retained, params.Prompt, subParams, timeout, true)
		case LaneShared:
		default:
			return startRetainedRun(runCtx, *t.Retained, params.Prompt, subParams, timeout, false)
		}
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
			return structuredTaskResult("timed out", params.Role, timeout, partial), nil
		case errors.Is(err, jobs.ErrStuckChild):
			return structuredTaskResult("stuck", params.Role, timeout, partial), nil
		case errors.Is(err, context.Canceled), runCtx.Err() == context.Canceled:
			return structuredTaskResult("cancelled", params.Role, timeout, partial), nil
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
func structuredTaskResult(reason, role string, timeout time.Duration, partial string) string {
	timeoutStr := ""
	if timeout > 0 {
		timeoutStr = fmt.Sprintf("%.0fs", timeout.Seconds())
	}
	out := struct {
		Error   string `json:"error"`
		Role    string `json:"role,omitempty"`
		Timeout string `json:"timeout,omitempty"`
		Partial string `json:"partial,omitempty"`
	}{
		Error:   reason,
		Role:    role,
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

// laneFor resolves the dispatch lane for a role. A nil LaneForRole keeps
// every dispatch on the proposal lane.
func (t *DelegateTool) laneFor(role string) DelegateLane {
	if t.LaneForRole == nil {
		return LaneProposal
	}
	if lane := t.LaneForRole(role); lane != "" {
		return lane
	}
	return LaneProposal
}
