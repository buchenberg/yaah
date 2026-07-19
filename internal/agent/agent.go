package agent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// Provider is the interface for model backends.
type Provider interface {
	Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error)
}

// StreamProvider is a provider that supports streaming responses.
type StreamProvider interface {
	Provider
	SendStream(ctx context.Context, req types.ChatRequest) (<-chan providers.StreamChunk, <-chan error)
}

// TokenCallback is called for each streamed token.
type TokenCallback func(token string)

// ToolInfo contains information about a tool call for display.
type ToolInfo struct {
	Name     string        // tool name
	Args     string        // abbreviated arguments
	Duration time.Duration // how long the tool took
	Result   string        // truncated tool result (only on second call)
	Error    string        // error message if the tool failed
}

// ToolCallback is called before and after each tool execution.
// The first call (before) has Duration=0 and Error="".
// The second call (after) has the actual Duration and any Error.
type ToolCallback func(info ToolInfo)

// SubAgentInfo contains information about a sub-agent dispatched by the
// task tool for display in the CLI/TUI.
type SubAgentInfo struct {
	Role     string        // worker, reviewer, planner, or custom
	Prompt   string        // abbreviated task prompt
	Duration time.Duration // how long the sub-agent ran (0 on start)
	Error    string        // error or status message on completion
}

// SubAgentCallback is called when a sub-agent starts (Duration=0) and
// when it completes (Duration set). Unlike ToolCallback, both calls are
// emitted by executeAndCollect so the CLI can bracket the sub-agent
// with visual markers independent of the task tool's own output.
type SubAgentCallback func(info SubAgentInfo)

// ThinkingCallback is called when the model outputs thinking/reasoning text.
type ThinkingCallback func(text string)

// FlushCallback is called when the model finishes a streaming segment and
// is about to start a tool call or a new iteration. The TUI uses this to
// flush the accumulated streaming content into the message list so the
// next segment starts on a fresh line.
type FlushCallback func(content string)

// ToolResultMaxLen is the maximum length of a tool result before truncation.
const ToolResultMaxLen = 8192

// Loop runs the agent conversation loop.
type Loop struct {
	Provider      Provider
	Registry      *tools.Registry
	SystemPrompt  string
	Model         string
	MaxIterations int
	OnToken       TokenCallback
	OnTool        ToolCallback
	OnSubAgent    SubAgentCallback
	OnThinking    ThinkingCallback
	OnFlush       FlushCallback
	Middleware    []Middleware // Optional custom middleware override

	// ContextWindow is the estimated token budget for the conversation.
	// When the total estimated tokens exceed 80% of this value, old messages
	// are compacted via LLM summarization (system prompt + recent messages are preserved).
	// Default 0 means no trimming.
	ContextWindow int

	// CompactionThreshold is the fraction of ContextWindow that triggers
	// compaction (e.g. 0.8 = 80%). Default 0 means 0.8.
	CompactionThreshold float64

	// MaxRetries is the number of retries on transient provider errors.
	// Default 0 means no retries.
	MaxRetries int

	// RetryBackoff is the base backoff duration. Default 1s.
	RetryBackoff time.Duration

	// TotalTokens accumulates token usage across all API calls in the loop.
	TotalTokens types.Usage

	// Messages holds the conversation history across multiple Run calls.
	Messages []types.Message

	// CompactProvider is used for context compaction summarization.
	// If nil, the main Provider is used.
	CompactProvider Provider

	// CompactModel is the model to use for compaction. If empty, Model is used.
	CompactModel string

	// LoopDetectCount is the number of identical tool calls (name+args+result hash)
	// required to trigger loop detection. Default 5.
	LoopDetectCount int

	// LoopDetectWindow is the size of the loop detection sliding window. Default 10.
	LoopDetectWindow int

	// ApprovalMode controls per-tool approval behavior: "allow", "ask", or "deny".
	ApprovalMode string

	// DB is the optional SQLite database for per-message persistence.
	// When non-nil, each message is persisted as it is appended to the
	// conversation, enabling session resume across process restarts.
	// When nil, the loop runs entirely in memory.
	DB *memory.DB

	// MsgIdx tracks the next message index for DB inserts.
	MsgIdx int

	// FollowUps is an optional channel for queuing follow-up messages while
	// the agent is running. Messages received are injected as user messages
	// at the start of the next iteration. Close this channel to unblock draining.
	FollowUps <-chan string

	// Steer is an optional channel for high-priority mid-turn input. Messages
	// received are injected immediately before the next provider call as a
	// user message, overriding the normal iteration flow.
	Steer <-chan string

	// PipelineNames is the ordered list of middleware names to use.
	// If non-empty, only these middleware run (subject to PipelineDisabled exclusions).
	// If empty, the default set (steer, followup, compaction, approval, loop_detection) is used.
	PipelineNames []string

	// PipelineDisabled is the set of middleware names to exclude from the pipeline.
	PipelineDisabled []string

	// MaxToolConcurrency caps concurrent tool goroutines. 0 means unlimited.
	// When > 0, a buffered channel semaphore is created by buildPipeline().
	MaxToolConcurrency int

	// PermissionRules is the list of permission rules for the PermissionMiddleware.
	PermissionRules []PermissionRule

	// MaxSubAgentDepth caps nested sub-agent calls. 0 means unlimited.
	MaxSubAgentDepth int

	// MaxSubAgentDepthByRole optionally caps task calls per sub-agent
	// role. A role absent from the map falls back to MaxSubAgentDepth.
	MaxSubAgentDepthByRole map[SubAgentRole]int

	// MaxSubAgentConcurrency caps the number of task tool calls that may
	// run simultaneously within a single iteration. It is a separate
	// semaphore from MaxToolConcurrency so sub-agent fan-out can be
	// bounded independently. 0 means unlimited.
	MaxSubAgentConcurrency int

	// PromptCaching enables Anthropic-style cache-control breakpoints on
	// system messages and recent tool results. Has no effect for non-Anthropic providers.
	PromptCaching bool

	// OtelEnabled enables OpenTelemetry spans for tool calls, sub-agent
	// dispatch, and LLM provider calls (via InstrumentedProvider).
	OtelEnabled bool

	// ApproveFn is an optional callback for tool approval in TUI contexts.
	// When set, it is called instead of the default stdin/stderr approveTool.
	// It receives the tool name and abbreviated args; returns true to approve.
	ApproveFn func(name, args string) bool

	toolSem chan struct{}

	// subAgentSem bounds concurrent task tool calls per iteration when
	// MaxSubAgentConcurrency > 0. Initialised by buildPipeline().
	subAgentSem chan struct{}

	// SessionID is a stable identifier for the session, set by the caller.
	// Used by emitHook to label events.
	SessionID string

	// HookDir is the directory where yaah writes JSONL hook event files.
	// When set, structured events are appended to <HookDir>/<session-id>.jsonl
	// on session boundaries, turn boundaries, and tool calls. Used by
	// external agents (e.g. entire-agent-yaah) for checkpoint/transcript
	// integration. Empty string means no hook events are written.
	// Must be set before Run() is called; must not change after.
	HookDir string

	hookOnce sync.Once
	hookOK   bool
	hookMu   sync.Mutex
	hookFile *os.File

	// loopHistory tracks recent tool call hashes for loop detection.
	loopHistory []string
}

// emitHook writes a structured JSONL line to the hook directory.
// It is a no-op when HookDir is empty. Failures are silent — hook
// emission must never break the agent loop.
func (l *Loop) emitHook(event HookEvent) {
	if l.HookDir == "" {
		return
	}
	l.hookOnce.Do(func() {
		if err := os.MkdirAll(l.HookDir, 0o755); err != nil {
			return
		}
		path := filepath.Join(l.HookDir, l.SessionID+".jsonl")
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		l.hookFile = f
		l.hookOK = true
	})
	if !l.hookOK {
		return
	}
	event.SessionID = l.SessionID
	if event.Timestamp == 0 {
		event.Timestamp = time.Now().UnixMilli()
	}
	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	l.hookMu.Lock()
	l.hookFile.Write(append(line, '\n'))
	l.hookMu.Unlock()
}

// closeHook closes the hook file if it was opened. Must be called after Run()
// completes to flush and release the file descriptor.
func (l *Loop) closeHook() {
	if l.hookFile != nil {
		l.hookFile.Close()
		l.hookFile = nil
	}
}

// persistMessage writes a single message to the database.
// No-op if DB is nil. Errors are logged to stderr but never returned,
// so the agent loop can continue even if the database is unavailable.
func (l *Loop) persistMessage(msg types.Message) {
	if l.DB == nil {
		return
	}
	content := msg.Content
	if content == "" {
		var parts []string
		for _, tc := range msg.ToolCalls {
			parts = append(parts, fmt.Sprintf("[tool:%s] %s", tc.Function.Name, tc.Function.Arguments))
		}
		content = strings.Join(parts, "\n")
	}
	toolCallsJSON := ""
	if len(msg.ToolCalls) > 0 {
		data, _ := json.Marshal(msg.ToolCalls)
		toolCallsJSON = string(data)
	}
	toolName := ""
	if msg.Role == "tool" {
		toolName = msg.Name
	}
	m := memory.Message{
		SessionID:  l.SessionID,
		Idx:        l.MsgIdx,
		Role:       msg.Role,
		Content:    content,
		ToolName:   toolName,
		ToolCallID: msg.ToolCallID,
		ToolCalls:  toolCallsJSON,
		Timestamp:  time.Now().Unix(),
	}
	if err := l.DB.AddMessage(m); err != nil {
		fmt.Fprintf(os.Stderr, "warning: db persist: %v\n", err)
		return
	}
	l.MsgIdx++
}

// buildPipeline assembles the middleware pipeline from config.
func (l *Loop) buildPipeline() *Pipeline {
	if len(l.Middleware) > 0 {
		return NewPipeline(l.Middleware...)
	}
	if l.MaxToolConcurrency > 0 {
		l.toolSem = make(chan struct{}, l.MaxToolConcurrency)
	}
	if l.MaxSubAgentConcurrency > 0 {
		l.subAgentSem = make(chan struct{}, l.MaxSubAgentConcurrency)
	}
	names := resolvedPipelineNames(l.PipelineNames, l.PipelineDisabled)
	mws := make([]Middleware, 0, len(names))
	for _, name := range names {
		if build, ok := builtinMiddleware[name]; ok {
			mws = append(mws, build(l))
		}
	}
	return NewPipeline(mws...)
}

// Run executes the full conversation loop for a single user message
// using the middleware pipeline.
func (l *Loop) Run(ctx context.Context, userInput string) (response string, runErr error) {
	if l.OtelEnabled {
		var rootSpan trace.Span
		ctx, rootSpan = observability.StartPrompt(ctx, userInput)
		defer func() {
			if runErr != nil {
				observability.RecordError(rootSpan, runErr)
			}
			rootSpan.End()
		}()
	}
	return l.runMiddleware(ctx, userInput)
}

// runMiddleware executes the agent loop using the middleware pipeline.
func (l *Loop) runMiddleware(ctx context.Context, userInput string) (response string, runErr error) {
	defer func() {
		l.closeHook()
		reason := "completed"
		if runErr != nil {
			reason = "error"
		}
		l.emitHook(HookEvent{
			Event:      SessionEnd,
			ExitReason: reason,
			Model:      l.Model,
		})
	}()

	l.applyDefaults()

	if l.Messages != nil {
		l.Messages = append(l.Messages, types.UserMsg(userInput))
		l.persistMessage(l.Messages[len(l.Messages)-1])
	} else {
		l.Messages = []types.Message{
			types.SystemMsg(l.SystemPrompt),
			types.UserMsg(userInput),
		}
		l.emitHook(HookEvent{
			Event: SessionStart,
			Model: l.Model,
		})
		l.persistMessage(l.Messages[0])
		l.persistMessage(l.Messages[1])
	}

	messages := l.Messages
	pipeline := l.buildPipeline()

	for iter := 0; iter < l.MaxIterations; iter++ {
		select {
		case <-ctx.Done():
			l.Messages = messages
			return "", ctx.Err()
		default:
		}

		l.emitHook(HookEvent{
			Event:  TurnStart,
			Prompt: userInput,
			Turn:   iter,
			Model:  l.Model,
		})

		var turnSpan trace.Span
		turnCtx := ctx
		if l.OtelEnabled {
			turnCtx, turnSpan = observability.StartTurn(ctx, iter, userInput)
		}

		step := &Step{
			Messages:     messages,
			Tools:        l.buildToolDefs(),
			Iteration:    iter,
			Model:        l.Model,
			SystemPrompt: l.SystemPrompt,
		}

		step, err := pipeline.RunPrepareStep(ctx, step)
		if err != nil {
			l.Messages = messages
			return "", err
		}
		messages = step.Messages

		req := types.ChatRequest{
			Model:    l.Model,
			Messages: messages,
			Tools:    step.Tools,
		}

		msg, streamed, err := l.getAssistantMessage(turnCtx, req)
		if err != nil {
			if turnSpan != nil {
				observability.RecordError(turnSpan, err)
				turnSpan.End()
			}
			l.Messages = messages
			return "", fmt.Errorf("provider error: %w", err)
		}
		messages = append(messages, msg)
		l.persistMessage(msg)

		if turnSpan != nil {
			turnSpan.SetAttributes(
				attribute.Bool("turn.streamed", streamed),
				attribute.Int("turn.tool_calls", len(msg.ToolCalls)),
				attribute.Int("turn.messages", len(messages)),
			)
		}

		if len(msg.ToolCalls) == 0 {
			if turnSpan != nil {
				turnSpan.End()
			}
			l.Messages = messages
			return msg.Content, nil
		}

		if streamed && msg.Content != "" && l.OnFlush != nil {
			l.OnFlush(msg.Content)
		}

		step, err = pipeline.RunPostModel(ctx, &msg, step)
		if err != nil {
			l.Messages = messages
			return "", err
		}

		toolResults := l.executeAndCollect(turnCtx, msg.ToolCalls, &messages)

		// Update step.Messages to reflect the tool results added by executeAndCollect
		step.Messages = messages

		// Update l.Messages so that CompactionMiddleware sees the latest messages
		l.Messages = messages

		step, err = pipeline.RunPostTool(ctx, toolResults, step)
		if err != nil {
			if turnSpan != nil {
				turnSpan.End()
			}
			l.Messages = messages
			return "", err
		}

		if turnSpan != nil {
			turnSpan.End()
		}

		messages = step.Messages
		for i := l.MsgIdx; i < len(messages); i++ {
			l.persistMessage(messages[i])
		}
	}

	l.Messages = messages
	return "", fmt.Errorf("max iterations (%d) reached", l.MaxIterations)
}

// applyDefaults sets default values for Loop fields.
func (l *Loop) applyDefaults() {
	if l.MaxIterations <= 0 {
		l.MaxIterations = 50
	}
	if l.Model == "" {
		l.Model = "gpt-4o-mini"
	}
	if l.RetryBackoff <= 0 {
		l.RetryBackoff = time.Second
	}
	if l.LoopDetectCount <= 0 {
		l.LoopDetectCount = 5
	}
	if l.LoopDetectWindow <= 0 {
		l.LoopDetectWindow = 10
	}
}

// executeAndCollect runs tool calls and returns ToolResult for middleware inspection.
func (l *Loop) executeAndCollect(ctx context.Context, calls []types.ToolCall, messages *[]types.Message) []ToolResult {
	results := make([]ToolResult, len(calls))
	ordered := make([]toolExecResult, len(calls))
	execResults := make(chan toolExecResult, len(calls))

	for i, tc := range calls {
		i, tc := i, tc

		if l.ApprovalMode == "deny" && toolIsDangerous(tc.Function.Name) {
			errMsg := fmt.Sprintf("error: tool %q requires approval but approval mode is 'deny'", tc.Function.Name)
			l.emitHook(HookEvent{Event: ToolStart, ToolName: tc.Function.Name, ToolArgs: tc.Function.Arguments})
			l.emitHook(HookEvent{Event: ToolEnd, ToolName: tc.Function.Name, ToolArgs: tc.Function.Arguments, ToolError: fmt.Sprintf("tool %q requires approval but approval mode is 'deny'", tc.Function.Name), ToolResult: errMsg})
			execResults <- toolExecResult{idx: i, callID: tc.ID, name: tc.Function.Name, args: tc.Function.Arguments, content: errMsg, err: fmt.Errorf("tool denied")}
			continue
		}
		if l.ApprovalMode == "ask" && toolIsDangerous(tc.Function.Name) {
			if !l.approveTool(tc.Function.Name, tc.Function.Arguments) {
				errMsg := fmt.Sprintf("error: tool %q was denied by user", tc.Function.Name)
				l.emitHook(HookEvent{Event: ToolStart, ToolName: tc.Function.Name, ToolArgs: tc.Function.Arguments})
				l.emitHook(HookEvent{Event: ToolEnd, ToolName: tc.Function.Name, ToolArgs: tc.Function.Arguments, ToolError: fmt.Sprintf("tool %q was denied by user", tc.Function.Name), ToolResult: errMsg})
				execResults <- toolExecResult{idx: i, callID: tc.ID, name: tc.Function.Name, args: tc.Function.Arguments, content: errMsg, err: fmt.Errorf("tool denied")}
				continue
			}
		}

		go func() {
			// Acquire the sub-agent semaphore before the general tool
			// semaphore so a task goroutine waiting on the sub-agent cap
			// does not hold a general tool slot and block unrelated
			// (non-task) calls in the same iteration.
			if tc.Function.Name == "task" && l.subAgentSem != nil {
				l.subAgentSem <- struct{}{}
				defer func() { <-l.subAgentSem }()
			}
			if l.toolSem != nil {
				l.toolSem <- struct{}{}
				defer func() { <-l.toolSem }()
			}
			abbreviated := abbreviateArgs(tc.Function.Arguments, 80)

			isTask := tc.Function.Name == "task"
			var taskRole, taskPrompt string
			if isTask && l.OnSubAgent != nil {
				taskRole, taskPrompt = parseTaskArgs(tc.Function.Arguments)
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
		results[i] = ToolResult{
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

// runLegacy executes the original inline agent loop (backward compatible).
// getAssistantMessage returns the next assistant message with retry logic.
// If the response has finish_reason="length" and tool calls, it errors rather
// than executing potentially truncated tool calls.
func (l *Loop) getAssistantMessage(ctx context.Context, req types.ChatRequest) (types.Message, bool, error) {
	var lastMsg types.Message
	var wasStreamed bool
	var lastErr error
	compactAttempts := 0

	for attempt := 0; attempt <= l.MaxRetries; attempt++ {
		var msg types.Message
		var streamed bool
		var err error

		if sp, ok := l.Provider.(StreamProvider); ok && l.OnToken != nil {
			msg, err = l.runStream(ctx, sp, req)
			streamed = true
		} else {
			var resp *types.ChatResponse
			resp, err = l.Provider.Send(ctx, req)
			if err == nil {
				l.captureUsage(resp)
				if len(resp.Choices) == 0 {
					err = fmt.Errorf("no choices in response")
				} else {
					msg = resp.Choices[0].Message
					if resp.Choices[0].FinishReason == "length" && len(msg.ToolCalls) > 0 {
						err = fmt.Errorf("response truncated (finish_reason=length), discarding %d tool calls", len(msg.ToolCalls))
						msg = types.Message{}
					}
				}
			}
		}

		if err == nil {
			return msg, streamed, nil
		}

		lastMsg = msg
		wasStreamed = streamed
		lastErr = err

		// Auto-compact on context overflow: if the provider rejected the
		// request because it exceeds the model's context window, compact
		// aggressively and retry without counting against MaxRetries.
		if isContextOverflowError(err) && l.ContextWindow > 0 && compactAttempts < 2 {
			beforeCount := len(l.Messages)
			l.compactContext(ctx, 0.5) // aggressive 50% threshold
			compactAttempts++
			// If compaction actually reduced message count, rebuild request and retry freely.
			if len(l.Messages) < beforeCount {
				req.Messages = l.Messages
				attempt-- // don't count against MaxRetries
				continue
			}
			// Fall through to normal backoff if compaction didn't help.
		}

		if attempt < l.MaxRetries {
			backoff := l.RetryBackoff * time.Duration(1<<attempt)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return types.Message{}, false, ctx.Err()
			}
		}
	}
	return lastMsg, wasStreamed, lastErr
}

// captureUsage adds response token usage to the running total.
func (l *Loop) captureUsage(resp *types.ChatResponse) {
	l.TotalTokens.PromptTokens += resp.Usage.PromptTokens
	l.TotalTokens.CompletionTokens += resp.Usage.CompletionTokens
	l.TotalTokens.TotalTokens += resp.Usage.TotalTokens
}

// EstimatedTokens returns the estimated token count for all messages.
func (l *Loop) EstimatedTokens() int {
	total := 0
	for _, m := range l.Messages {
		total += len(m.Content)
		for _, tc := range m.ToolCalls {
			total += len(tc.Function.Arguments) + len(tc.Function.Name)
		}
	}
	return total / 4
}

// toolExecResult holds the outcome of a single tool execution.
type toolExecResult struct {
	idx     int
	callID  string
	name    string
	args    string
	content string
	dur     time.Duration
	err     error
}

// isContextOverflowError returns true if the error indicates the provider
// rejected the request because it exceeds the model's context window.
func isContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, pattern := range []string{
		"context length",
		"too many tokens",
		"reduce the length",
		"maximum context",
		"max tokens",
		"token limit",
		"prompt is too long",
		"context window",
		"requested token count",
	} {
		if strings.Contains(msg, pattern) {
			return true
		}
	}
	return false
}

// toolCallHash returns a SHA-256 hash of tool name, arguments, and result for loop detection.
// Including args prevents false positives when the same tool returns identical success
// messages for different inputs (e.g. writing different files).
func toolCallHash(name, args, content string) string {
	h := sha256.New()
	h.Write([]byte(name))
	h.Write([]byte{0})
	h.Write([]byte(args))
	h.Write([]byte{0})
	h.Write([]byte(content))
	return hex.EncodeToString(h.Sum(nil))
}

// parseTaskArgs extracts a display-friendly role and prompt from a task
// tool's JSON arguments. Returns ("default", prompt-abbreviation) when
// the role is empty or the JSON is unparseable.
func parseTaskArgs(args string) (role, prompt string) {
	var p struct {
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
		Role        string `json:"role"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return "default", ""
	}
	role = p.Role
	if role == "" {
		role = "default"
	}
	return role, abbreviateArgs(p.Description, 60)
}

// compactContext checks if the estimated token count exceeds the given
// fraction of ContextWindow. If threshold is 0, defaults to 0.8 (80%).
// If over budget, it uses the LLM to summarize old messages into a
// structured summary, preserving the system message and recent turns.
// Falls back to simple trimming if the LLM call fails or returns empty.
func (l *Loop) compactContext(ctx context.Context, threshold float64) {
	if threshold <= 0 {
		threshold = 0.8
	}
	target := int(float64(l.ContextWindow) * threshold)
	totalChars := 0
	for _, m := range l.Messages {
		totalChars += len(m.Content)
		for _, tc := range m.ToolCalls {
			totalChars += len(tc.Function.Arguments) + len(tc.Function.Name)
		}
	}
	if totalChars/4 <= target {
		return
	}

	sysMsg := l.Messages[0]
	rest := l.Messages[1:]
	if len(rest) <= 4 {
		return
	}

	keepRecent := 6
	if len(rest) <= keepRecent {
		return
	}

	split := len(rest) - keepRecent
	oldMsgs := rest[:split]
	keepMsgs := rest[split:]

	var sb strings.Builder
	sb.WriteString("Summarize the following conversation excerpt. Keep the structured format below.\n\n")
	sb.WriteString("## Goal\n")
	sb.WriteString("(what the user is working on)\n\n")
	sb.WriteString("## Completed Work\n")
	sb.WriteString("(what was accomplished)\n\n")
	sb.WriteString("## Active Work\n")
	sb.WriteString("(what is in progress)\n\n")
	sb.WriteString("## Pending Tasks\n")
	sb.WriteString("(what still needs to be done)\n\n")
	sb.WriteString("## Key Decisions\n")
	sb.WriteString("(important decisions made)\n\n")
	sb.WriteString("## Files Modified\n")
	sb.WriteString("(list of files that were read, edited, or created)\n\n")
	sb.WriteString("---\n")
	sb.WriteString("Conversation excerpt to summarize:\n\n")
	for _, m := range oldMsgs {
		if m.Content != "" {
			sb.WriteString(fmt.Sprintf("%s: %s\n", m.Role, m.Content))
		}
		for _, tc := range m.ToolCalls {
			sb.WriteString(fmt.Sprintf("[tool:%s] %s\n", tc.Function.Name, tc.Function.Arguments))
		}
	}

	compactProvider := l.CompactProvider
	if compactProvider == nil {
		compactProvider = l.Provider
	}
	compactModel := l.CompactModel
	if compactModel == "" {
		compactModel = l.Model
	}

	req := types.ChatRequest{
		Model: compactModel,
		Messages: []types.Message{
			types.UserMsg(sb.String()),
		},
	}

	resp, err := compactProvider.Send(ctx, req)
	if err != nil || len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		l.trimContext()
		return
	}

	summary := resp.Choices[0].Message.Content

	newMsgs := []types.Message{sysMsg}
	if l.SystemPrompt == "" {
		newMsgs[0] = types.SystemMsg(summary)
	} else {
		newMsgs = append(newMsgs, types.SystemMsg("Previous conversation summary:\n"+summary))
	}
	newMsgs = append(newMsgs, keepMsgs...)
	l.Messages = newMsgs
}

// trimContext removes old messages when the estimated token count exceeds
// 80% of ContextWindow. Preserves the system message and recent exchanges.
// This is a fallback when LLM-powered compaction is unavailable.
func (l *Loop) trimContext() {
	target := l.ContextWindow * 4 / 5
	totalChars := 0
	for _, m := range l.Messages {
		totalChars += len(m.Content)
		for _, tc := range m.ToolCalls {
			totalChars += len(tc.Function.Arguments) + len(tc.Function.Name)
		}
	}
	if totalChars/4 <= target {
		return
	}

	sysMsg := l.Messages[0]
	rest := l.Messages[1:]
	for len(rest) > 0 && totalChars/4 > target {
		removed := len(rest[0].Content)
		for _, tc := range rest[0].ToolCalls {
			removed += len(tc.Function.Arguments) + len(tc.Function.Name)
		}
		totalChars -= removed
		rest = rest[1:]
	}

	newMsgs := make([]types.Message, 1, len(rest)+1)
	newMsgs[0] = sysMsg
	newMsgs = append(newMsgs, rest...)
	l.Messages = newMsgs
}

// runStream handles a streaming request and returns the assembled assistant
// message (content + any tool calls). Tool calls accumulated from the stream
// are returned to Run, which executes them exactly like the non-streaming
// path. Content deltas are emitted via OnToken as they arrive.
// If finish_reason is "length" and tool calls are present, returns an error
// to prevent executing truncated tool calls.
func (l *Loop) runStream(ctx context.Context, sp StreamProvider, req types.ChatRequest) (types.Message, error) {
	var streamSpan trace.Span
	if l.OtelEnabled {
		ctx, streamSpan = observability.StartStream(ctx, req.Model)
	}

	start := time.Now()
	chunks, errs := sp.SendStream(ctx, req)

	var content strings.Builder
	toolCallMap := make(map[int]*types.ToolCall)
	var finishReason string
	var firstToken bool
	var tokenCount int

	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				if streamSpan != nil {
					observability.FinishStream(streamSpan, 0, tokenCount, len(toolCallMap))
					streamSpan.End()
				}
				return l.checkTruncatedStream(content.String(), toolCallMap, finishReason)
			}

			if !firstToken {
				firstToken = true
				if streamSpan != nil {
					streamSpan.SetAttributes(attribute.Int64("llm.ttft_ms", time.Since(start).Milliseconds()))
				}
			}

			if len(chunk.Choices) == 0 {
				continue
			}

			delta := chunk.Choices[0].Delta

			if delta.ReasoningContent != "" && l.OnThinking != nil {
				l.OnThinking(delta.ReasoningContent)
			}

			if delta.Content != "" {
				content.WriteString(delta.Content)
				tokenCount++
				if l.OnToken != nil {
					l.OnToken(delta.Content)
				}
			}

			for _, tc := range delta.ToolCalls {
				idx := tc.Index
				if existing, ok := toolCallMap[idx]; ok {
					existing.Function.Arguments += tc.Function.Arguments
					if tc.ID != "" {
						existing.ID = tc.ID
					}
					if tc.Function.Name != "" {
						existing.Function.Name = tc.Function.Name
					}
				} else {
					newTC := types.ToolCall{
						Index: idx,
						ID:    tc.ID,
						Type:  tc.Type,
						Function: types.ToolCallFn{
							Name:      tc.Function.Name,
							Arguments: tc.Function.Arguments,
						},
					}
					toolCallMap[idx] = &newTC
				}
			}

			if chunk.Choices[0].FinishReason != nil {
				finishReason = *chunk.Choices[0].FinishReason
			}

		case err := <-errs:
			if err != nil {
				if streamSpan != nil {
					observability.RecordError(streamSpan, err)
					streamSpan.End()
				}
				return types.Message{}, err
			}
			if streamSpan != nil {
				observability.FinishStream(streamSpan, 0, tokenCount, len(toolCallMap))
				streamSpan.End()
			}
			return l.checkTruncatedStream(content.String(), toolCallMap, finishReason)

		case <-ctx.Done():
			if streamSpan != nil {
				observability.RecordError(streamSpan, ctx.Err())
				streamSpan.End()
			}
			return types.Message{}, ctx.Err()
		}

		if finishReason != "" {
			break
		}
	}

	if streamSpan != nil {
		observability.FinishStream(streamSpan, 0, tokenCount, len(toolCallMap))
		streamSpan.End()
	}
	return l.checkTruncatedStream(content.String(), toolCallMap, finishReason)
}

// checkTruncatedStream returns the assembled message or an error if the
// stream was truncated (finish_reason=length) with pending tool calls.
func (l *Loop) checkTruncatedStream(content string, toolCallMap map[int]*types.ToolCall, finishReason string) (types.Message, error) {
	msg := l.assembleStreamed(content, toolCallMap)
	if finishReason == "length" && len(msg.ToolCalls) > 0 {
		return types.Message{}, fmt.Errorf("streamed response truncated (finish_reason=length), discarding %d tool calls", len(msg.ToolCalls))
	}
	return msg, nil
}

// assembleStreamed builds the assistant message from accumulated stream state,
// ordering tool calls by their delta index.
func (l *Loop) assembleStreamed(content string, toolCalls map[int]*types.ToolCall) types.Message {
	msg := types.Message{
		Role:    "assistant",
		Content: content,
	}
	if len(toolCalls) > 0 {
		indices := make([]int, 0, len(toolCalls))
		for idx := range toolCalls {
			indices = append(indices, idx)
		}
		sort.Ints(indices)
		for _, idx := range indices {
			msg.ToolCalls = append(msg.ToolCalls, *toolCalls[idx])
		}
	}
	return msg
}

// buildToolDefs builds the OpenAI-format tool definitions from the registry.
func (l *Loop) buildToolDefs() []types.ToolDef {
	toolNames := l.Registry.List()
	toolDefs := make([]types.ToolDef, 0, len(toolNames))
	for _, name := range toolNames {
		t := l.Registry.Get(name)
		if t == nil {
			continue
		}
		toolDefs = append(toolDefs, types.ToolDef{
			Type: "function",
			Function: types.ToolFn{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  json.RawMessage(t.Schema()),
			},
		})
	}
	return toolDefs
}

// abbreviateArgs truncates JSON args to maxLen characters with ellipsis.
// Handles multi-byte UTF-8 by counting runes, not bytes.
func abbreviateArgs(args string, maxLen int) string {
	runes := []rune(args)
	if len(runes) <= maxLen {
		return args
	}
	return string(runes[:maxLen-3]) + "..."
}

// dangerousTools is the set of tool names that require approval.
var dangerousTools = map[string]bool{
	"bash":       true,
	"powershell": true,
	"write":      true,
	"edit":       true,
	"delete":     true,
}

// toolIsDangerous returns true if the tool requires user approval.
func toolIsDangerous(name string) bool {
	return dangerousTools[name]
}

// approveTool prompts the user on stderr/stdin to approve a tool call.
// Returns true if the user approves. If ApproveFn is set, it delegates to that instead.
func (l *Loop) approveTool(name, args string) bool {
	abbr := abbreviateArgs(args, 120)
	if l.ApproveFn != nil {
		return l.ApproveFn(name, abbr)
	}
	fmt.Fprintf(os.Stderr, "\n  ⚠ Approve %s(%s)? [y/N]: ", name, abbr)
	os.Stderr.Sync()

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 1024), 1024)
	if scanner.Scan() {
		input := strings.ToLower(strings.TrimSpace(scanner.Text()))
		return input == "y" || input == "yes"
	}
	return false
}
