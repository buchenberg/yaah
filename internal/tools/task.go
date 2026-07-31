package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/types"
)

// subAgentModelKey stores the sub-agent's model name in the context so
// the caller can read it after execution.
const subAgentModelKey contextKey = "yaah-subagent-model"

// SubAgentModelFromContext returns the model used by a completed sub-agent.
func SubAgentModelFromContext(ctx context.Context) string {
	if v := ctx.Value(subAgentModelKey); v != nil {
		return v.(string)
	}
	return ""
}

// subAgentModelPtrKey is a context key for a *string the runner writes
// the sub-agent's model into.
type subAgentModelPtrKey struct{}

// WithSubAgentModelPtr stores ptr in ctx so the sub-agent runner can write
// the model name into it. Call after runner returns to read the value.
func WithSubAgentModelPtr(ctx context.Context, ptr *string) context.Context {
	return context.WithValue(ctx, subAgentModelPtrKey{}, ptr)
}

// WriteSubAgentModel writes model to the *string stored in ctx, if present.
func WriteSubAgentModel(ctx context.Context, model string) {
	if ptr, ok := ctx.Value(subAgentModelPtrKey{}).(*string); ok {
		*ptr = model
	}
}

// subAgentStartKey is a context key for the start notifier the runner
// fires once the sub-agent's provider and model are resolved, just
// before its loop begins.
type subAgentStartKey struct{}

// WithSubAgentStartNotifier stores fn in ctx so the sub-agent runner can
// announce the resolved model at start time. The caller uses this to
// emit a start event that already knows which model the child runs on.
func WithSubAgentStartNotifier(ctx context.Context, fn func(model string)) context.Context {
	return context.WithValue(ctx, subAgentStartKey{}, fn)
}

// NotifySubAgentStart invokes the start notifier stored in ctx, if
// present. Call from the runner after the sub-agent model is resolved.
func NotifySubAgentStart(ctx context.Context, model string) {
	if fn, ok := ctx.Value(subAgentStartKey{}).(func(string)); ok {
		fn(model)
	}
}

// subAgentUsageKey stores a *types.Usage pointer for accumulating
// sub-agent token usage in the caller.
type subAgentUsageKey struct{}

// WithSubAgentUsage sets a usage accumulator in ctx so the caller can
// collect sub-agent token counts.
func WithSubAgentUsage(ctx context.Context, usage *types.Usage) context.Context {
	return context.WithValue(ctx, subAgentUsageKey{}, usage)
}

// AddSubAgentUsage adds delta to the usage accumulator in ctx, if present.
func AddSubAgentUsage(ctx context.Context, delta types.Usage) {
	if acc, ok := ctx.Value(subAgentUsageKey{}).(*types.Usage); ok {
		acc.PromptTokens += delta.PromptTokens
		acc.CompletionTokens += delta.CompletionTokens
		acc.TotalTokens += delta.TotalTokens
	}
}

// subAgentHeartbeatKey is a context key for the per-sub-agent heartbeat
// channel. The sub-agent loop non-blocking-sends on this channel each
// iteration so a parent watchdog can detect stuck children.
type subAgentHeartbeatKey struct{}

// WithSubAgentHeartbeat stores hb in ctx so the sub-agent loop can emit
// heartbeats. The caller should create a buffered channel (cap 1) so the
// sub-agent never blocks on send.
func WithSubAgentHeartbeat(ctx context.Context, hb chan struct{}) context.Context {
	return context.WithValue(ctx, subAgentHeartbeatKey{}, hb)
}

// SendHeartbeat non-blocking-sends on the heartbeat channel stored in ctx,
// if present. Designed to be called at the top of each agent loop iteration.
func SendHeartbeat(ctx context.Context) {
	if hb, ok := ctx.Value(subAgentHeartbeatKey{}).(chan struct{}); ok {
		select {
		case hb <- struct{}{}:
		default:
		}
	}
}

// ErrStuckChild is returned when a sub-agent is cancelled by the parent
// watchdog after StuckChildTimeout elapses with no heartbeat.
var ErrStuckChild = errors.New("sub-agent stuck: no heartbeat received within deadline")

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

	// MaxTurns, when > 0, overrides the soft turn cap for tool-using turns.
	MaxTurns int

	// JSONMode enables structured JSON output for this sub-agent.
	JSONMode bool

	// OutputLimit caps the sub-agent's final synthesized result in bytes.
	// 0 means use the role/config default.
	OutputLimit int
}

// EscalationSeverity classifies how serious a sub-agent escalation is.
type EscalationSeverity string

const (
	EscalationInfo     EscalationSeverity = "info"
	EscalationWarning  EscalationSeverity = "warning"
	EscalationBlocker  EscalationSeverity = "blocker"
	EscalationCritical EscalationSeverity = "critical"
)

// Escalation represents a structured issue raised by a sub-agent that
// could not complete its task due to a blocker, error, or caveat.
type Escalation struct {
	Severity   EscalationSeverity `json:"severity"`
	Summary    string             `json:"summary"`
	Detail     string             `json:"detail,omitempty"`
	Suggestion string             `json:"suggestion,omitempty"`
}

// SubAgentOutput is the structured result from a completed sub-agent.
// It wraps the raw output string with any parsed escalation and error.
type SubAgentOutput struct {
	RawOutput  string      `json:"raw_output"`
	Escalation *Escalation `json:"escalation,omitempty"`
	Error      string      `json:"error,omitempty"`
}

var escalationPattern = regexp.MustCompile(
	"(?s)```escalation\\s*\\n(.*?)\\n```",
)

// ParseSubAgentOutput extracts structured escalation data from a sub-agent's
// final output string. Returns the raw output and any parsed escalation.
func ParseSubAgentOutput(output string, runErr error) *SubAgentOutput {
	result := &SubAgentOutput{RawOutput: output}
	if runErr != nil {
		result.Error = runErr.Error()
	}

	m := escalationPattern.FindStringSubmatch(output)
	if len(m) < 2 {
		return result
	}

	var esc Escalation
	if err := json.Unmarshal([]byte(m[1]), &esc); err != nil {
		return result
	}
	result.Escalation = &esc
	return result
}

// TaskRunner runs a sub-agent for a given prompt and role configuration
// and returns its final response. The runner is responsible for building
// the role-specific tool registry and Loop.
type TaskRunner func(ctx context.Context, prompt string, params SubAgentParams) (string, error)

// BackgroundResultNotifier is called when a background sub-agent completes.
// role is the sub-agent role name, description is the task description,
// result is the final output, and err is any error from the runner.
type BackgroundResultNotifier func(role, description, result string, err error)

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

	// BackgroundNotifier is called when a background sub-agent completes.
	// The caller wires this to push results into the parent's follow-up
	// channel so they appear as injected user messages. When nil,
	// background mode is disabled (the tool will error if background
	// is requested without a notifier).
	BackgroundNotifier BackgroundResultNotifier
}

func (t *TaskTool) Name() string { return "spawn_subagent" }
func (t *TaskTool) Description() string {
	return prompts.ToolDescription("task")
}

func (t *TaskTool) Schema() json.RawMessage {
	known := t.roleNames()
	if len(known) > 0 {
		return BuildTaskSchema(known, t.RoleDescriptions)
	}
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"description": {"type": "string", "description": "3-5 word description of the subtask"},
			"prompt": {"type": "string", "description": "The task for the sub-agent to perform autonomously"},
			"role": {"type": "string", "description": "Sub-agent role selecting its tool set and limits. Required. Use list_subagents to see available roles."},
			"max_iterations": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Optional cap on sub-agent loop turns. Overrides the role default."},
			"max_turns": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Optional soft cap on tool-using turns. Overrides the role default."},
			"json_mode": {"type": "boolean", "description": "Request structured JSON output from the sub-agent."},
			"output_limit": {"type": "integer", "minimum": 1024, "description": "Optional byte cap on the sub-agent's final report."},
			"background": {"type": "boolean", "description": "When true, dispatch the sub-agent asynchronously and return immediately. Results arrive in a follow-up message."}
		},
		"required": ["description", "prompt", "role"]
	}`)
}

// roleNames returns the known role names for validation and schema
// generation: the cached startup snapshot layered with the live resolver
// (if any) so role create/delete via the role tool takes immediate effect.
func (t *TaskTool) roleNames() []string {
	// Build the set of known roles: start with the cached snapshot,
	// then layer on the live resolver so role create/delete takes
	// immediate effect.
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

// BuildTaskSchema returns a JSON Schema for the task tool whose role
// enum is populated from the given list of role names, so user-defined
// roles discovered at runtime are visible to the model.
func BuildTaskSchema(roleNames []string, roleDescriptions map[string]string) json.RawMessage {
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
				"description": "3-5 word description of the subtask",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "The task for the sub-agent to perform autonomously",
			},
			"role": map[string]any{
				"type":        "string",
				"enum":        roles,
				"description": roleDesc,
			},
			"max_iterations": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     50,
				"description": "Optional cap on sub-agent loop turns. Overrides the role default.",
			},
			"max_turns": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     50,
				"description": "Optional soft cap on tool-using turns. Overrides the role default.",
			},
			"json_mode": map[string]any{
				"type":        "boolean",
				"description": "Request structured JSON output from the sub-agent.",
			},
			"output_limit": map[string]any{
				"type":        "integer",
				"minimum":     1024,
				"description": "Optional byte cap on the sub-agent's final report.",
			},
			"background": map[string]any{
				"type":        "boolean",
				"description": "When true, dispatch the sub-agent asynchronously and return immediately. Results arrive in a follow-up message.",
			},
		},
		"required": []string{"description", "prompt", "role"},
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
		MaxTurns       int    `json:"max_turns"`
		JSONMode       bool   `json:"json_mode"`
		OutputLimit    int    `json:"output_limit"`
		Background     bool   `json:"background"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
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
		return "", fmt.Errorf("spawn_subagent: unknown role %q — valid roles: %s", params.Role, strings.Join(known, ", "))
	}

	if t.Runner == nil {
		return "", fmt.Errorf("spawn_subagent: sub-agent runner not configured")
	}

	if params.Background && t.BackgroundNotifier == nil {
		return "", fmt.Errorf("spawn_subagent: background mode requested but not available")
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
		MaxTurns:       params.MaxTurns,
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
		notifier := t.BackgroundNotifier
		runner := t.Runner
		role := params.Role
		desc := params.Description
		bgCtx := context.WithoutCancel(runCtx)
		if timeout > 0 {
			var cancel context.CancelFunc
			bgCtx, cancel = context.WithTimeout(bgCtx, timeout)
			go func() {
				<-ctx.Done()
				cancel()
			}()
		}
		go func() {
			result, err := runner(bgCtx, params.Prompt, subParams)
			if bgCtx.Err() != nil && err == nil {
				err = bgCtx.Err()
			}
			notifier(role, desc, result, err)
		}()
		return structuredBackgroundResult(label), nil
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
		case errors.Is(err, ErrStuckChild):
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

// structuredBackgroundResult builds a JSON payload for background sub-agent
// dispatch. The model sees this as the immediate tool result; the actual
// sub-agent output arrives later via a follow-up message.
func structuredBackgroundResult(label string) string {
	out := struct {
		Status string `json:"status"`
		Label  string `json:"label"`
	}{
		Status: "running",
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
