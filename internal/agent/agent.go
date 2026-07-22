package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/agent/llm"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// Provider is the interface for model backends.
type Provider = llm.Provider

// StreamProvider is a provider that supports streaming responses.
type StreamProvider = llm.StreamProvider

// TokenCallback is called for each streamed token.
type TokenCallback = llm.TokenCallback

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
	Model    string        // model used by the sub-agent
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
type ThinkingCallback = llm.ThinkingCallback

// ToolsLevel controls which tools the agent sees.
type ToolsLevel int

const (
	// FullTools is the default — all tools from the registry.
	FullTools ToolsLevel = iota
	// SubAgentsOnly gives only the task tool — the agent coordinates through sub-agents.
	SubAgentsOnly
)

// FlushCallback is called when the model finishes a streaming segment and
// is about to start a tool call or a new iteration. The TUI uses this to
// flush the accumulated streaming content into the message list so the
// next segment starts on a fresh line.
type FlushCallback func(content string)

// ToolResultMaxLen is the maximum length of a tool result before truncation.
const ToolResultMaxLen = 8192

// pruneMessageMaxLen is the threshold above which old messages are pruned
// before being sent to the LLM summarizer during compaction.
const pruneMessageMaxLen = 2000

// minContextFloor is the minimum trigger threshold for compaction, preventing
// over-aggressive compaction on small-window models.
const minContextFloor = 64000

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
	Middleware    []pipeline.Middleware // Optional custom middleware override

	// LLM wraps the provider with streaming, retry, fallback, and compaction.
	LLM *llm.Client

	// ToolsLevel controls tool visibility: FullTools gives all registry
	// tools; SubAgentsOnly gives only the task tool for coordination.
	ToolsLevel ToolsLevel

	// ContextWindow is the estimated token budget for the conversation.
	// When the estimated tokens exceed the compaction threshold, old messages
	// are compacted via LLM summarization (system prompt + recent messages are preserved).
	// Default 0 means no trimming.
	ContextWindow int

	// CompactionThreshold is the fraction of ContextWindow that triggers
	// compaction (e.g. 0.5 = 50%). Default 0 means 0.5.
	CompactionThreshold float64

	// EstimateFactor is the multiplier applied to the chars/4 token estimate
	// for preflight compaction checks. Provider tokenizers systematically
	// undercount code and JSON payloads; 1.3 compensates. 0 means use the
	// default (1.3).
	EstimateFactor float64

	// MaxRetries is the number of retries on transient provider errors.
	// Default 0 means no retries.
	MaxRetries int

	// RetryBackoff is the base backoff duration. Default 1s.
	RetryBackoff time.Duration

	// TotalTokens accumulates token usage across all API calls in the loop.
	TotalTokens types.Usage

	// LastPromptTokens is the prompt token count from the most recent
	// API call — used for context compaction decisions (avoiding the
	// inaccurate chars/4 estimate).
	LastPromptTokens int

	// TotalReasoningTokens accumulates reasoning token usage for observability.
	TotalReasoningTokens int

	// TotalCachedPromptTokens accumulates cached prompt tokens for observability.
	TotalCachedPromptTokens int

	// Messages holds the conversation history across multiple Run calls.
	Messages []types.Message

	// CompactProvider is used for context compaction summarization.
	// If nil, the main Provider is used.
	CompactProvider Provider

	// CompactModel is the model to use for compaction. If empty, Model is used.
	CompactModel string

	// FallbackProvider is an alternative model backend used when the
	// primary provider returns auth, billing, or rate-limit errors.
	// If nil, retries continue with the primary provider.
	FallbackProvider Provider

	// FallbackModel is the model name to use with FallbackProvider.
	// When empty, Model is used.
	FallbackModel string

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

	// MaxInlineToolsPerTurn caps the number of inline tool calls the
	// planner may issue in a single turn. When exceeded, excess calls
	// are dropped and a warning is injected into the conversation so the
	// model learns to break work into smaller batches or delegate.
	// 0 means unlimited. Default: 0 (use with models prone to tool spam).
	MaxInlineToolsPerTurn int

	// PermissionRules is the list of permission rules for the PermissionMiddleware.
	PermissionRules []pipeline.PermissionRule

	// MaxSubAgentDepth caps nested sub-agent calls. 0 means unlimited.
	MaxSubAgentDepth int

	// MaxSubAgentDepthByRole optionally caps task calls per sub-agent
	// role. A role absent from the map falls back to MaxSubAgentDepth.
	MaxSubAgentDepthByRole map[subagent.SubAgentRole]int

	// MaxSubAgentConcurrency caps the number of task tool calls that may
	// run simultaneously within a single outer-loop turn. 0 means unlimited.
	MaxSubAgentConcurrency int

	// PromptCaching enables Anthropic-style cache-control breakpoints on
	// system prompt and recent messages.
	PromptCaching bool

	// Pruner soft-prunes stale tool-result content from provider requests
	// (Tier-0 context reclaim). Default-constructed in applyDefaults; disable
	// via PipelineDisabled: ["soft_prune"].
	Pruner *pipeline.Pruner

	// ConflictTracker detects and reports external file modifications made
	// outside the agent's own write/edit/replace/delete tools during a turn.
	// When non-nil and conflicts are found, a user message describing them
	// is appended at the end of the turn.
	ConflictTracker *tools.ConflictTracker

	// OtelEnabled enables OpenTelemetry tracing and metrics collection.
	// When true, each Run call creates a root span and child spans for
	// each turn, tool execution, and provider call.
	OtelEnabled bool

	// OtelVerbose enables verbose Jaeger trace recording of full assistant
	// responses (including streamed messages) and per-turn conversation state.
	// Useful for debugging the agent loop.
	OtelVerbose bool

	// ApproveFn is an optional callback for custom approval UI (TUI/REPL/etc.).
	// When set, approveTool delegates to this function; otherwise it uses
	// the default stdin/stderr prompt.
	ApproveFn func(name, args string) bool `json:"-"`

	// SessionID identifies the conversation session for persistence and logging.
	SessionID string

	// HookDir is the directory for a best-effort JSONL event log.
	HookDir string

	// PreviousSummary stores the last LLM-generated conversation summary for
	// incremental compaction, so each summarization only needs to cover new
	// messages rather than re-summarizing the entire history.
	PreviousSummary string

	// SystemPromptOverride is an optional override for the system prompt.
	// When set, the Loop will use this prompt instead of one assembled from
	// instructions and provider defaults.
	SystemPromptOverride string

	// hookFile is the open file descriptor for JSONL event hooks.
	hookFile *os.File

	// hookOnce guards the one-time creation of the hook file.
	hookOnce sync.Once

	// hookOK is false if the hook file could not be opened.
	hookOK bool

	// hookMu serializes writes to the hook file.
	hookMu sync.Mutex

	// toolSem is a semaphore channel for limiting concurrent tool executions.
	// Created in applyDefaults when MaxToolConcurrency > 0.
	toolSem chan struct{}

	// subAgentSem is a semaphore channel for limiting concurrent task calls.
	// Created in applyDefaults when MaxSubAgentConcurrency > 0.
	subAgentSem chan struct{}

	// lastCompactionTokens tracks the estimated token count after the most
	// recent compaction, used to prevent re-compacting too aggressively.
	lastCompactionTokens int

	// ineffectiveCompactions counts successive compactions that saved < 10%
	// of tokens. When >= 2, compaction is skipped.
	ineffectiveCompactions int

	// usageMu serializes addUsage calls from concurrent delegate dispatches.
	usageMu sync.Mutex
}

// buildPipeline assembles the middleware pipeline from config.
func (l *Loop) buildPipeline() *pipeline.Pipeline {
	if len(l.Middleware) > 0 {
		return pipeline.NewPipeline(l.Middleware...)
	}
	return pipeline.NewFromConfig(l.toPipelineConfig())
}

// Compact satisfies the pipeline.Compactor interface by delegating to
// the Loop's context compaction machinery. It syncs step messages into
// l.Messages, compacts, and returns the result.
func (l *Loop) Compact(ctx context.Context, messages []types.Message, threshold float64) []types.Message {
	l.Messages = messages
	l.compactContext(ctx, threshold)
	return l.Messages
}

// toPipelineConfig builds a PipelineConfig from the Loop's current settings.
func (l *Loop) toPipelineConfig() pipeline.PipelineConfig {
	return pipeline.PipelineConfig{
		Steer:                  l.Steer,
		FollowUps:              l.FollowUps,
		ContextWindow:          l.ContextWindow,
		CompactionThreshold:    l.CompactionThreshold,
		Compactor:              l,
		ApprovalMode:           l.ApprovalMode,
		PermissionRules:        l.PermissionRules,
		LoopDetectCount:        l.LoopDetectCount,
		LoopDetectWindow:       l.LoopDetectWindow,
		MaxToolConcurrency:     l.MaxToolConcurrency,
		MaxSubAgentDepth:       l.MaxSubAgentDepth,
		MaxSubAgentDepthByRole: l.MaxSubAgentDepthByRole,
		PromptCaching:          l.PromptCaching,
		Pruner:                 l.Pruner,
		PruneHooks:             l.pruneHooks(),
		PipelineNames:          l.PipelineNames,
		PipelineDisabled:       l.PipelineDisabled,
	}
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
	pipe := l.buildPipeline()

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

		step := &pipeline.Step{
			Messages:     messages,
			Tools:        l.buildToolDefs(),
			Iteration:    iter,
			Model:        l.Model,
			SystemPrompt: l.SystemPrompt,
		}

		step, err := pipe.RunPrepareStep(ctx, step)
		if err != nil {
			l.Messages = messages
			return "", err
		}
		messages = step.Messages

		req := types.ChatRequest{
			Model:    l.Model,
			Messages: l.applyPruning(messages),
			Tools:    l.buildToolsForLevel(),
		}

		// Verbose: record the conversation the model is about to see
		// so the full message history is visible in Jaeger.
		if l.OtelVerbose && turnSpan != nil {
			observability.RecordConversation(turnSpan, messages)
		}

		// Pre-flight context guard: compact before sending if context has
		// exceeded the absolute window (last-resort safety net for between-turn
		// growth from large tool results).
		if l.ContextWindow > 0 && l.LastPromptTokens > l.ContextWindow {
			l.compactContext(turnCtx, 0.5)
			messages = l.Messages
			req.Messages = messages
		}

		tokensBeforeTurn := l.TotalTokens

		msg, streamed, usage, err := l.LLM.Call(turnCtx, req)
		if err != nil {
			if turnSpan != nil {
				observability.RecordError(turnSpan, err)
				turnSpan.End()
			}
			l.Messages = messages
			return "", fmt.Errorf("provider error: %w", err)
		}
		l.addUsage(usage)
		messages = append(messages, msg)
		l.persistMessage(msg)

		if turnSpan != nil {
			toolNames := make([]string, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				toolNames = append(toolNames, tc.Function.Name)
			}
			turnAttrs := []attribute.KeyValue{
				attribute.Bool("turn.streamed", streamed),
				attribute.Int("turn.iteration", iter),
				attribute.Int("turn.tool_calls", len(msg.ToolCalls)),
				attribute.String("turn.tool_call_names", strings.Join(toolNames, ",")),
				attribute.Int("turn.messages", len(messages)),
				attribute.Int("llm.total_prompt_tokens", l.TotalTokens.PromptTokens),
				attribute.Int("llm.total_completion_tokens", l.TotalTokens.CompletionTokens),
				attribute.Int("turn.prompt_tokens", l.TotalTokens.PromptTokens-tokensBeforeTurn.PromptTokens),
				attribute.Int("turn.completion_tokens", l.TotalTokens.CompletionTokens-tokensBeforeTurn.CompletionTokens),
			}
			if l.TotalReasoningTokens > 0 {
				turnAttrs = append(turnAttrs, attribute.Int("llm.total_reasoning_tokens", l.TotalReasoningTokens))
			}
			if l.TotalCachedPromptTokens > 0 {
				turnAttrs = append(turnAttrs, attribute.Int("llm.total_cached_prompt_tokens", l.TotalCachedPromptTokens))
			}
			turnSpan.SetAttributes(turnAttrs...)
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

		step, err = pipe.RunPostModel(ctx, &msg, step)
		if err != nil {
			l.Messages = messages
			return "", err
		}

		// Inline tool execution
		calls := msg.ToolCalls
		if l.MaxInlineToolsPerTurn > 0 && len(calls) > l.MaxInlineToolsPerTurn {
			dropped := len(calls) - l.MaxInlineToolsPerTurn
			calls = calls[:l.MaxInlineToolsPerTurn]
			if dropped > 0 {
				warning := fmt.Sprintf(
					"[system] %d tool call(s) dropped — inline limit is %d per turn. "+
						"Break large batches into smaller turns or use the delegate tool for batch work.",
					dropped, l.MaxInlineToolsPerTurn,
				)
				messages = append(messages, types.UserMsg(warning))
				if l.OtelVerbose && turnSpan != nil {
					turnSpan.AddEvent("inline.truncated", trace.WithAttributes(
						attribute.Int("inline.dropped", dropped),
					))
				}
			}
		}

		if l.OtelVerbose && turnSpan != nil {
			names := make([]string, len(calls))
			for i, tc := range calls {
				names[i] = tc.Function.Name
			}
			turnSpan.AddEvent("dispatch.inline", trace.WithAttributes(
				attribute.Int("inline.count", len(calls)),
				attribute.String("inline.tool_names", strings.Join(names, ",")),
			))
		}
		toolResults := l.executeAndCollect(turnCtx, calls, &messages)
		step.Messages = messages

		if l.ConflictTracker != nil {
			l.emitHook(HookEvent{
				Event: ConflictCheck,
				Turn:  iter,
				Model: l.Model,
			})

			if report := l.ConflictTracker.DetectAndReset(); report != "" {
				fileCount := strings.Count(report, "File: ")
				l.emitHook(HookEvent{
					Event:         ConflictDetect,
					Turn:          iter,
					Model:         l.Model,
					ConflictFiles: fileCount,
				})
				if turnSpan != nil {
					turnSpan.SetAttributes(attribute.Int("conflict.files", fileCount))
					turnSpan.AddEvent("conflict.detected", trace.WithAttributes(
						attribute.Int("conflict.files", fileCount),
					))
				}
				conflictMsg := types.UserMsg(report)
				messages = append(messages, conflictMsg)
				step.Messages = messages
				l.Messages = messages
				l.persistMessage(conflictMsg)
			}
		}

		step, err = pipe.RunPostTool(ctx, toolResults, step)
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
	l.ensurePruner()
	if l.MaxIterations <= 0 {
		l.MaxIterations = 50
	}
	if l.Model == "" {
		l.Model = "deepseek-v4-pro"
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
	if l.MaxToolConcurrency > 0 && l.toolSem == nil {
		l.toolSem = make(chan struct{}, l.MaxToolConcurrency)
	}
	if l.MaxSubAgentConcurrency > 0 && l.subAgentSem == nil {
		l.subAgentSem = make(chan struct{}, l.MaxSubAgentConcurrency)
	}
	if l.LLM == nil {
		l.LLM = &llm.Client{
			Provider:         l.Provider,
			FallbackProvider: l.FallbackProvider,
			Model:            l.Model,
			FallbackModel:    l.FallbackModel,
			MaxRetries:       l.MaxRetries,
			RetryBackoff:     l.RetryBackoff,
			ContextWindow:    l.ContextWindow,
			OnToken:          l.OnToken,
			OnThinking:       l.OnThinking,
			Compact:          l.llmCompact,
			Trim:             l.llmTrim,
			OtelEnabled:      l.OtelEnabled,
			OtelVerbose:      l.OtelVerbose,
		}
	}
}

func (l *Loop) buildToolsForLevel() []types.ToolDef {
	switch l.ToolsLevel {
	case SubAgentsOnly:
		return l.agentTools()
	default:
		return l.buildToolDefs()
	}
}

func (l *Loop) agentTools() []types.ToolDef {
	agentToolNames := map[string]bool{"spawn_subagent": true, "list_subagents": true}
	var defs []types.ToolDef
	for _, name := range l.Registry.List() {
		if agentToolNames[name] {
			t := l.Registry.Get(name)
			if t == nil {
				continue
			}
			defs = append(defs, types.ToolDef{
				Type: "function",
				Function: types.ToolFn{
					Name:        t.Name(),
					Description: t.Description(),
					Parameters:  json.RawMessage(t.Schema()),
				},
			})
		}
	}
	return defs
}

func (l *Loop) addUsage(u types.Usage) {
	l.usageMu.Lock()
	defer l.usageMu.Unlock()
	l.TotalTokens.PromptTokens += u.PromptTokens
	l.TotalTokens.CompletionTokens += u.CompletionTokens
	l.TotalTokens.TotalTokens += u.TotalTokens
	l.LastPromptTokens = u.PromptTokens
	if d := u.CompletionTokensDetails; d != nil {
		l.TotalReasoningTokens += d.ReasoningTokens
	}
	if d := u.PromptTokensDetails; d != nil {
		l.TotalCachedPromptTokens += d.CachedTokens
	}
}

func (l *Loop) llmCompact(ctx context.Context, messages []types.Message, threshold float64) []types.Message {
	l.Messages = messages
	l.compactContext(ctx, threshold)
	return l.Messages
}

// llmTrim reduces context deterministically by removing the oldest
// messages. It is used as a fallback when the LLM returns an empty
// stream, indicating the context is too large even for summarization.
func (l *Loop) llmTrim(ctx context.Context, messages []types.Message) []types.Message {
	l.Messages = messages
	l.trimContext()
	return l.Messages
}
