package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/dispatch"
	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/permissions"
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

// PendingProposalScopeIDs lists the open retained proposals' scope IDs —
// the scope reaper's live-set complement: an unsettled proposal keeps its
// scope active on purpose (yaah-95ri.4).
func (t *DelegateTool) PendingProposalScopeIDs() []string {
	if t == nil || t.Retained == nil {
		return nil
	}
	return t.Retained.PendingProposalScopeIDs()
}

// LiveDispatchScopeIDs lists every scope the reaper must spare that the
// Registry cannot see: in-flight container/worktree dispatch scopes plus
// pending retained proposals (yaah-95ri.6).
func (t *DelegateTool) LiveDispatchScopeIDs() []string {
	if t == nil || t.Retained == nil {
		return nil
	}
	return t.Retained.InFlightDispatchScopeIDs()
}
func (t *DelegateTool) Description() string {
	return prompts.ToolDescription("delegate")
}

func (t *DelegateTool) Execute(ctx context.Context, args string) (string, error) {
	// Strict keys: a typo such as "backgroud", "seed_form" or "max_turn"
	// used to be ignored and silently change the dispatch (foreground
	// instead of background, fresh instead of threaded, role default
	// instead of the requested cap). Reject it with the allowed list.
	strict, err := DecodeStrictObject("delegate", args,
		"description", "prompt", "role", "timeout_seconds", "max_iterations",
		"max_turns", "json_mode", "output_limit", "background", "seed_from",
		"seed_mode", "provider", "model")
	if err != nil {
		return "", err
	}
	strictJSON, err := json.Marshal(strict)
	if err != nil {
		return "", fmt.Errorf("delegate: invalid arguments: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(strictJSON, &raw); err != nil {
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
		SeedFrom       string `json:"seed_from"`
		SeedMode       string `json:"seed_mode"`
		Provider       string `json:"provider"`
		Model          string `json:"model"`
	}
	if err := json.Unmarshal(fixed, &params); err != nil {
		return "", fmt.Errorf("delegate: invalid arguments: %w", err)
	}
	if strings.TrimSpace(params.Description) == "" {
		return "", fmt.Errorf("delegate: description is required — a 3-5 word summary of the subtask")
	}
	if params.Prompt == "" {
		return "", fmt.Errorf("delegate: prompt is required")
	}
	if len(params.Prompt) > maxDelegatePromptBytes {
		return "", fmt.Errorf("delegate: prompt is %d bytes; the maximum is %d — split the task or summarize the context", len(params.Prompt), maxDelegatePromptBytes)
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

	// Clamp every model-supplied numeric override to the bounds advertised
	// in the schema (delegate_schema.go) before it reaches the runner: schema
	// bounds are advisory — nothing in the transport enforces them — so a
	// runaway or injected payload can otherwise send a negative value or 10^6
	// and neutralize the deadline, iterate forever, or blank the report.
	// 0 is the "unset, the role decides" sentinel for all four params, so a
	// clamped-away value lands on 0 rather than on an invented default: only
	// values actually supplied are clamped.
	//
	// The role's min_turns / min_iterations floor is deliberately NOT applied
	// here — DelegateTool has no view of the role profile or the per-role
	// config. It is enforced downstream when the runner resolves the budget
	// (internal/agent/runner → internal/agent/budget.Resolve), where a
	// declared floor beats a per-call override and can even grow iterations to
	// protect it. That is what keeps the schema's "cannot go below the role's
	// min_turns" promise true.
	clampedTimeout := clampTimeoutSeconds(params.TimeoutSeconds)
	clampedIter := clampMaxLoopCycles(params.MaxLoopCycles)
	clampedTurns := clampMaxToolTurns(params.MaxTurns)
	clampedOutput := clampOutputLimit(params.OutputLimit)

	subParams := jobs.SubAgentParams{
		Role:           params.Role,
		TimeoutSeconds: clampedTimeout,
		MaxLoopCycles:  clampedIter,
		MaxToolTurns:   clampedTurns,
		JSONMode:       params.JSONMode,
		OutputLimit:    clampedOutput,
		SeedFrom:       params.SeedFrom,
		SeedMode:       params.SeedMode,
		Provider:       params.Provider,
		Model:          params.Model,
	}

	timeout := resolveTaskTimeout(clampedTimeout, t.ResolveTimeout, subParams)

	label := params.Role
	if params.Description != "" {
		label = label + " — " + params.Description
	}

	// Resolve the lane once: LaneForRole reads the live role registry, so a
	// second call could disagree with the first if a role is created or
	// edited mid-dispatch.
	lane := t.laneFor(params.Role)
	conflictLane := ConflictLaneShared
	if t.Retained != nil && lane != LaneShared {
		conflictLane = ConflictLaneRetained
	}
	subParams.Lane = conflictLane
	runCtx := WithConflictDispatch(ctx, label, conflictLane)

	// Background jobs cancel from the session root, not the dispatching call,
	// so ordinary context values do not flow through. Re-attach the conflict
	// dispatch (with a fresh id for the job) and the inherited permission
	// ceiling (parity W5.7) before the runner starts — a deferred dispatch must
	// not become the loophole that escapes what the parent denied.
	res, err := dispatch.Run(runCtx, dispatch.DispatchRequest{
		Description:    params.Description,
		Prompt:         params.Prompt,
		Lane:           lane,
		Params:         subParams,
		Timeout:        timeout,
		Background:     params.Background,
		Runner:         t.Runner,
		Retained:       t.Retained,
		BackgroundJobs: t.BackgroundJobs,
		BackgroundContextSeeder: func(jobCtx context.Context) context.Context {
			jobCtx = WithConflictDispatch(jobCtx, label, conflictLane)
			return permissions.WithInherited(jobCtx, permissions.Inherited(runCtx))
		},
	})
	if err != nil {
		return "", err
	}
	return formatDispatchResult(res)
}

// classifyRunnerResult maps a runner outcome to the orchestrator-facing
// result. A non-empty result alongside an error is treated as the partial
// output the sub-agent produced before stopping.
func (t *DelegateTool) classifyRunnerResult(result string, err error, role string, timeout time.Duration, runCtx context.Context) (string, error) {
	return formatDispatchResult(dispatch.ClassifyRunnerResult(result, err, role, timeout, runCtx))
}

// formatDispatchResult renders the typed core's status into the model-facing
// tool frames. The core owns execution and classification; this adapter owns
// only the wire shape.
func formatDispatchResult(res dispatch.DispatchResult) (string, error) {
	switch res.Status {
	case dispatch.DispatchCompleted:
		return res.Result, nil
	case dispatch.DispatchProposal:
		if res.Proposal == nil {
			return "", fmt.Errorf("delegate: proposal result is missing its envelope")
		}
		return dispatch.MarshalProposalEnvelope(*res.Proposal), nil
	case dispatch.DispatchRunning:
		return structuredBackgroundResult(res.JobID, res.Label), nil
	case dispatch.DispatchTimedOut:
		return structuredTaskResult("timed out", res.Role, res.Timeout, res.Partial), nil
	case dispatch.DispatchStuck:
		return structuredTaskResult("stuck", res.Role, res.Timeout, res.Partial), nil
	case dispatch.DispatchCancelled:
		return structuredTaskResult("cancelled", res.Role, res.Timeout, res.Partial), nil
	case dispatch.DispatchFailed:
		if res.Err != nil {
			return "", res.Err
		}
		return "", fmt.Errorf("delegate: dispatch failed")
	default:
		return "", fmt.Errorf("delegate: unknown dispatch status %q", res.Status)
	}
}

// maxDelegatePromptBytes bounds one dispatch prompt. The prompt is sent as a
// single user message, so an unbounded payload can defeat the context-window
// budget before the sub-agent's first turn. 64 KiB leaves room for a large
// code snippet while keeping the failure a clear tool error, not a transport
// or context overflow.
const maxDelegatePromptBytes = 64 << 10

// clampTimeoutSeconds caps the model-supplied timeout at the schema's
// 600s maximum so a runaway or injected value cannot pin a sub-agent
// open indefinitely. Any positive value below the maximum is honoured
// as-is; 0 means "use the role default" and is preserved, matching the
// schema's advertised 0–600 window.
func clampTimeoutSeconds(v int) int {
	if v > 600 {
		return 600
	}
	if v < 0 {
		return 0
	}
	return v
}

// clampMaxLoopCycles enforces the schema's 1–100 window. Values above 100
// are capped; 0 is preserved (means "use the role default").
func clampMaxLoopCycles(v int) int {
	if v > 100 {
		return 100
	}
	if v < 0 {
		return 0
	}
	return v
}

// clampMaxToolTurns enforces the schema's 1–50 window for the soft turn
// cap. Values above 50 are capped; any value below the window falls back
// to 0 ("use the role default") instead of being raised to 1, because
// max_turns=1 means "one tool turn then answer" — a meaning a negative or
// injected value must not be able to acquire. The role's own min_turns
// floor is applied later, when the runner resolves the budget.
func clampMaxToolTurns(v int) int {
	if v > 50 {
		return 50
	}
	if v < 0 {
		return 0
	}
	return v
}

// clampOutputLimit enforces the schema's 1024-byte minimum on the report
// cap. 0 and negative values mean "use the role/config default" and are
// preserved as 0; a smaller positive value is raised to the minimum so an
// injected sub-kilobyte cap cannot blank the sub-agent's report. The schema
// advertises no maximum, so none is enforced here — a generous cap costs
// nothing beyond output the run already paid for, unlike an unbounded loop.
func clampOutputLimit(v int) int {
	const minOutputLimit = 1024
	if v <= 0 {
		return 0
	}
	if v < minOutputLimit {
		return minOutputLimit
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

// backgroundDispatchEnvelope is the immediate result of a background dispatch.
type backgroundDispatchEnvelope struct {
	Status string `json:"status"`
	JobID  string `json:"job_id"`
	Label  string `json:"label"`
}

// structuredBackgroundResult builds a JSON payload for background sub-agent
// dispatch. The model sees this as the immediate tool result carrying a job
// id it can use with the subagent_jobs tool (status/cancel); the actual
// sub-agent output arrives later via a follow-up message.
func structuredBackgroundResult(jobID, label string) string {
	out := backgroundDispatchEnvelope{
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

// dispatchFailureEnvelope is the payload returned when a dispatch did not
// complete normally.
type dispatchFailureEnvelope struct {
	Error   string `json:"error"`
	Role    string `json:"role,omitempty"`
	Timeout string `json:"timeout,omitempty"`
	Partial string `json:"partial,omitempty"`
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
	out := dispatchFailureEnvelope{
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
