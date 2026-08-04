package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/agent/llm"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/pubsub"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// Provider is the interface for model backends.
type Provider = llm.Provider

// StreamProvider is a provider that supports streaming responses.
type StreamProvider = llm.StreamProvider

// ToolInfo is a legacy type preserved for external reference.
// New code should consume ToolStartEvent / ToolEndEvent via the Broker.
type ToolInfo struct {
	Name     string        // tool name
	Args     string        // abbreviated arguments
	Duration time.Duration // how long the tool took
	Result   string        // truncated tool result (only on second call)
	Error    string        // error message if the tool failed
}

// SubAgentInfo is a legacy type preserved for external reference.
// New code should consume SubAgentStartEvent / SubAgentEndEvent via the Broker.
type SubAgentInfo struct {
	Role     string        // worker, reviewer, planner, or custom
	Model    string        // model used by the sub-agent
	Prompt   string        // abbreviated task prompt
	Duration time.Duration // how long the sub-agent ran (0 on start)
	Error    string        // error or status message on completion
}

// ToolsLevel controls which tools the agent sees.
type ToolsLevel int

const (
	// FullTools is the default — all tools from the registry.
	FullTools ToolsLevel = iota
	// SubAgentsOnly gives only the task tool — the agent coordinates through sub-agents.
	SubAgentsOnly
)

// ToolResultMaxLen is a deprecated alias for defaultTruncateMaxBytes. Use
// Loop.truncateToolResult() in agent_truncation.go for the line/byte dual-limit
// truncation.
const ToolResultMaxLen = defaultTruncateMaxBytes

// pruneMessageMaxLen is the threshold above which old messages are pruned
// before being sent to the LLM summarizer during compaction.
const pruneMessageMaxLen = 2000

// minContextFloor is the minimum trigger threshold for compaction, preventing
// over-aggressive compaction on small-window models.
const minContextFloor = 64000

// Loop runs the agent conversation loop.
type Loop struct {
	Config LoopConfig
	State  LoopState

	Provider Provider
	Registry *tools.Registry
	// View receives agent events (tokens, thinking, flush, tool starts/ends,
	// sub-agent starts/ends, done). When set, the loop internally creates a
	// pubsub.Broker and BrokerView adapter; callers should NOT set Broker.
	View View

	broker     *pubsub.Broker[Event]
	brokerView *BrokerView

	Middleware []pipeline.Middleware
	LLM        *llm.Client

	CompactProvider  Provider
	FallbackProvider Provider
	FallbackModel    string

	Persister *SessionPersister
	Hooks     *HookEmitter

	FollowUps <-chan string
	Steer     <-chan string

	ApproveFn       func(name, args string) bool `json:"-"`
	ConflictTracker *tools.ConflictTracker
	CtxMgr          *ContextManager

	toolConcurrency *pipeline.ToolConcurrencyMiddleware
	subAgentSem     chan struct{}

	toolDefsCache []types.ToolDef
	toolDefsGen   int

	usageMu   sync.Mutex
	toolIDGen atomic.Int64
}

// LoopConfig holds immutable configuration set once before Run.
type LoopConfig struct {
	Model                  string
	MaxLoopCycles          int
	MaxToolTurns           int
	JSONMode               bool
	ToolsLevel             ToolsLevel
	ContextWindow          int
	CompactionThreshold    float64
	RawCompactionThreshold float64
	CompactMaxMessages     int
	EstimateFactor         float64
	QualityGates           map[string][]string
	MaxRetries             int
	RetryBackoff           time.Duration
	LoopDetectCount        int
	LoopDetectWindow       int
	ApprovalMode           string
	WrapUpThreshold        int
	MaxInlineToolsPerTurn  int
	MaxToolConcurrency     int
	MaxSubAgentConcurrency int
	StuckChildTimeout      time.Duration
	StuckChildTimeouts     map[string]time.Duration
	PromptCaching          bool
	CompactModel           string
	SessionID              string
	PipelineNames          []string
	PipelineDisabled       []string
	PermissionRules        []pipeline.PermissionRule
	OtelEnabled            bool
	OtelVerbose            bool
	SystemPrompt           string
	SystemPromptOverride   string
}

// LoopState holds mutable runtime state modified during Run.
type LoopState struct {
	Messages                   []types.Message
	TotalTokens                types.Usage
	LastPromptTokens           int
	LastCachedPromptTokens     int
	TotalReasoningTokens       int
	TotalCachedPromptTokens    int
	LastFinishReason           string
	LastResponseModel          string
	PreviousSummary            string
	LastCompactionTokens       int
	IneffectiveCompactions     int
	CompactionForcedByOverflow bool
	CompactionBudgetMultiplier float64
	CompactionSavingsHistory   []float64
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
// l.State.Messages, compacts, and returns the result.
func (l *Loop) Compact(ctx context.Context, messages []types.Message, threshold float64) []types.Message {
	l.State.Messages = messages
	l.compactContext(ctx, threshold)
	return l.State.Messages
}

// toPipelineConfig builds a PipelineConfig from the Loop's current settings.
func (l *Loop) toPipelineConfig() pipeline.PipelineConfig {
	return pipeline.PipelineConfig{
		Steer:                  l.Steer,
		FollowUps:              l.FollowUps,
		ContextWindow:          l.Config.ContextWindow,
		CompactionThreshold:    l.Config.CompactionThreshold,
		Compactor:              l,
		ApprovalMode:           l.Config.ApprovalMode,
		PermissionRules:        l.Config.PermissionRules,
		LoopDetectCount:        l.Config.LoopDetectCount,
		LoopDetectWindow:       l.Config.LoopDetectWindow,
		MaxToolConcurrency:     l.Config.MaxToolConcurrency,
		MaxSubAgentConcurrency: l.Config.MaxSubAgentConcurrency,
		PromptCaching:          l.Config.PromptCaching,
		Pruner:                 l.CtxMgr.Pruner,
		PruneHooks:             l.pruneHooks(),
		PipelineNames:          l.Config.PipelineNames,
		PipelineDisabled:       l.Config.PipelineDisabled,
	}
}

// Run executes the full conversation loop for a single user message
// using the middleware pipeline.
func (l *Loop) Run(ctx context.Context, userInput string) (response string, runErr error) {
	if l.Config.OtelEnabled {
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
	defer l.publishDone(&response, &runErr)
	defer l.teardown(&runErr)

	l.applyDefaults()
	l.initMessages(userInput)

	messages := l.State.Messages
	pipe := l.buildPipeline()

	for iter := 0; iter < l.Config.MaxLoopCycles; iter++ {
		select {
		case <-ctx.Done():
			l.State.Messages = messages
			return "", ctx.Err()
		default:
		}

		tools.SendHeartbeat(ctx)

		l.Hooks.Emit(HookEvent{
			Event:  TurnStart,
			Prompt: userInput,
			Turn:   iter,
			Model:  l.Config.Model,
		})

		var turnSpan trace.Span
		turnCtx := ctx
		if l.Config.OtelEnabled {
			turnCtx, turnSpan = observability.StartTurn(ctx, iter, userInput)
		}

		step, req, err := l.buildTurnRequest(ctx, iter, messages, pipe, turnSpan)
		if err != nil {
			l.State.Messages = messages
			return "", err
		}
		messages = step.Messages

		if err := l.guardContextBeforeCall(turnCtx, &messages, &req, turnSpan); err != nil {
			return "", err
		}

		tokensBeforeTurn := l.State.TotalTokens

		result, err := l.LLM.Call(turnCtx, req)
		if err != nil {
			if turnSpan != nil {
				observability.RecordError(turnSpan, err)
				turnSpan.End()
			}
			l.State.Messages = messages
			return "", fmt.Errorf("provider error: %w", err)
		}
		msg := result.Message
		l.Provider = l.LLM.Provider
		l.Config.Model = l.LLM.Model
		l.FallbackProvider = l.LLM.FallbackProvider
		l.FallbackModel = l.LLM.FallbackModel
		l.addUsage(result.Usage)
		l.State.LastFinishReason = result.FinishReason
		l.State.LastResponseModel = result.ResponseModel
		messages = append(messages, msg)
		l.Persister.Persist(msg)

		if turnSpan != nil {
			l.recordTurnSpanAttrs(turnSpan, messages, msg, tokensBeforeTurn, iter, result.Streamed)
		}

		if len(msg.ToolCalls) == 0 {
			if turnSpan != nil {
				turnSpan.End()
			}
			l.State.Messages = messages
			return msg.Content, nil
		}

		if result.Streamed && msg.Content != "" && l.broker != nil {
			l.broker.PublishMustDeliver(&FlushEvent{Content: msg.Content})
		}

		step, err = pipe.RunPostModel(ctx, &msg, step)
		if err != nil {
			l.State.Messages = messages
			return "", err
		}

		err = l.executeToolPhase(turnCtx, iter, msg, &messages, &step, pipe, turnSpan)
		if err != nil {
			return "", err
		}

		if turnSpan != nil {
			turnSpan.End()
		}

		messages = step.Messages
		for i := l.Persister.MsgIdx(); i < len(messages); i++ {
			l.Persister.Persist(messages[i])
		}
	}

	l.State.Messages = messages
	return "", fmt.Errorf("max iterations (%d) reached", l.Config.MaxLoopCycles)
}

// publishDone publishes the final DoneEvent to the broker view.
func (l *Loop) publishDone(response *string, runErr *error) {
	if l.brokerView == nil {
		return
	}
	var done DoneEvent
	done.Response = *response
	if *runErr != nil {
		done.Error = (*runErr).Error()
	}
	done.ContextTokens = l.EstimatedTokens()
	done.ContextWindow = l.Config.ContextWindow
	done.FinishReason = l.State.LastFinishReason
	done.ResponseModel = l.State.LastResponseModel
	done.Usage = l.State.TotalTokens
	if l.State.TotalReasoningTokens > 0 {
		done.Usage.CompletionTokensDetails = &types.CompletionTokensDetails{
			ReasoningTokens: l.State.TotalReasoningTokens,
		}
	}
	if l.State.TotalCachedPromptTokens > 0 {
		done.Usage.PromptTokensDetails = &types.PromptTokensDetails{
			CachedTokens: l.State.TotalCachedPromptTokens,
		}
	}
	l.broker.PublishMustDeliver(&done)
	l.brokerView.Close()
}

// teardown handles panic recovery, flushes the persister, closes hooks,
// and emits the session-end event.
func (l *Loop) teardown(runErr *error) {
	if r := recover(); r != nil {
		*runErr = fmt.Errorf("panic: %v", r)
	}
	l.Persister.Flush()
	l.Hooks.Close()
	reason := "completed"
	if *runErr != nil {
		reason = "error"
	}
	l.Hooks.Emit(HookEvent{
		Event:      SessionEnd,
		ExitReason: reason,
		Model:      l.Config.Model,
	})
}

// initMessages appends the user input to the conversation, persisting
// messages and emitting session-start hooks for new conversations.
func (l *Loop) initMessages(userInput string) {
	if l.State.Messages != nil {
		l.State.Messages = append(l.State.Messages, types.UserMsg(userInput))
		l.Persister.Persist(l.State.Messages[len(l.State.Messages)-1])
	} else {
		l.State.Messages = []types.Message{
			types.SystemMsg(l.Config.SystemPrompt),
			types.UserMsg(userInput),
		}
		l.Hooks.Emit(HookEvent{
			Event: SessionStart,
			Model: l.Config.Model,
		})
		l.Persister.Persist(l.State.Messages[0])
		l.Persister.Persist(l.State.Messages[1])
	}
}

// buildTurnRequest runs PrepareStep middleware, builds the ChatRequest
// with MaxTurns/WrapUp logic and JSONMode, and records verbosely if asked.
func (l *Loop) buildTurnRequest(ctx context.Context, iter int, messages []types.Message, pipe *pipeline.Pipeline, turnSpan trace.Span) (*pipeline.Step, types.ChatRequest, error) {
	step := &pipeline.Step{
		Messages:      messages,
		Tools:         l.buildToolDefs(),
		Iteration:     iter,
		MaxToolTurns:  l.Config.MaxToolTurns,
		MaxLoopCycles: l.Config.MaxLoopCycles,
		Model:         l.Config.Model,
		SystemPrompt:  l.Config.SystemPrompt,
	}

	var err error
	step, err = pipe.RunPrepareStep(ctx, step)
	if err != nil {
		return nil, types.ChatRequest{}, err
	}
	messages = step.Messages

	req := types.ChatRequest{
		Model:    l.Config.Model,
		Messages: l.prepareRequestMessages(messages),
		Tools:    l.buildToolsForLevel(),
	}

	if l.Config.MaxToolTurns > 0 {
		effective := l.Config.MaxToolTurns
		if effective >= l.Config.MaxLoopCycles {
			effective = l.Config.MaxLoopCycles - 1
		}
		if iter >= effective {
			req.Tools = nil
			if l.Config.OtelEnabled && turnSpan != nil {
				turnSpan.AddEvent("maxturns.stripped", trace.WithAttributes(
					attribute.Int("maxturns.limit", l.Config.MaxToolTurns),
					attribute.Int("maxturns.iteration", iter),
				))
			}
		} else if l.Config.WrapUpThreshold > 0 && iter >= effective-l.Config.WrapUpThreshold {
			l.injectWrapUpNotice(&req, turnSpan, effective-iter)
		}
	} else if l.Config.WrapUpThreshold > 0 && iter >= l.Config.MaxLoopCycles-l.Config.WrapUpThreshold {
		l.injectWrapUpNotice(&req, turnSpan, l.Config.MaxLoopCycles-iter)
	}

	if l.Config.JSONMode {
		req.ResponseFormat = &types.ResponseFormat{Type: "json_object"}
	}

	return step, req, nil
}

// guardContextBeforeCall applies pre-flight context compaction when the
// estimated token count exceeds the context window or the serialized
// request exceeds the payload byte limit. Returns an error when the
// request ends up empty after compaction (unrecoverable).
func (l *Loop) guardContextBeforeCall(turnCtx context.Context, messages *[]types.Message, req *types.ChatRequest, turnSpan trace.Span) error {
	if l.Config.OtelVerbose && turnSpan != nil {
		observability.RecordConversation(turnSpan, *messages)
	}

	if l.Config.ContextWindow > 0 && l.State.LastPromptTokens > l.Config.ContextWindow {
		l.compactContext(turnCtx, 0.5)
		*messages = l.State.Messages
		req.Messages = l.prepareRequestMessages(*messages)
	}

	if l.Config.ContextWindow > 0 && estimatePayloadBytes(req.Messages, req.Tools) > maxPayloadBytes {
		l.compactContext(turnCtx, 0.5)
		*messages = l.State.Messages
		req.Messages = l.prepareRequestMessages(*messages)
	}

	if len(req.Messages) == 0 {
		err := fmt.Errorf("refusing to send empty message list to provider — %d messages after prepare", len(req.Messages))
		if turnSpan != nil {
			observability.RecordError(turnSpan, err)
			turnSpan.End()
		}
		l.State.Messages = *messages
		return err
	}

	return nil
}

// recordTurnSpanAttrs populates OTel span attributes for the current
// turn: tool call counts, token usage, and model info.
func (l *Loop) recordTurnSpanAttrs(turnSpan trace.Span, messages []types.Message, msg types.Message, tokensBeforeTurn types.Usage, iter int, streamed bool) {
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
		attribute.String("llm.model", l.Config.Model),
		attribute.Int("llm.total_prompt_tokens", l.State.TotalTokens.PromptTokens),
		attribute.Int("llm.total_completion_tokens", l.State.TotalTokens.CompletionTokens),
		attribute.Int("turn.prompt_tokens", l.State.TotalTokens.PromptTokens-tokensBeforeTurn.PromptTokens),
		attribute.Int("turn.completion_tokens", l.State.TotalTokens.CompletionTokens-tokensBeforeTurn.CompletionTokens),
	}
	if l.State.TotalReasoningTokens > 0 {
		turnAttrs = append(turnAttrs, attribute.Int("llm.total_reasoning_tokens", l.State.TotalReasoningTokens))
	}
	if l.State.TotalCachedPromptTokens > 0 {
		turnAttrs = append(turnAttrs, attribute.Int("llm.total_cached_prompt_tokens", l.State.TotalCachedPromptTokens))
	}
	turnSpan.SetAttributes(turnAttrs...)
}

// executeToolPhase truncates inline tools to MaxInlineToolsPerTurn,
// executes them via executeAndCollect, runs conflict detection,
// and invokes the PostTool middleware step.
func (l *Loop) executeToolPhase(turnCtx context.Context, iter int, msg types.Message, messages *[]types.Message, step **pipeline.Step, pipe *pipeline.Pipeline, turnSpan trace.Span) error {
	calls := msg.ToolCalls
	if l.Config.MaxInlineToolsPerTurn > 0 && len(calls) > l.Config.MaxInlineToolsPerTurn {
		dropped := len(calls) - l.Config.MaxInlineToolsPerTurn
		calls = calls[:l.Config.MaxInlineToolsPerTurn]
		if dropped > 0 {
			warning := fmt.Sprintf(
				"[system] %d tool call(s) dropped — inline limit is %d per turn. "+
					"Break large batches into smaller turns or use the delegate tool for batch work.",
				dropped, l.Config.MaxInlineToolsPerTurn,
			)
			*messages = append(*messages, types.UserMsg(warning))
			if l.Config.OtelVerbose && turnSpan != nil {
				turnSpan.AddEvent("inline.truncated", trace.WithAttributes(
					attribute.Int("inline.dropped", dropped),
				))
			}
		}
	}

	if l.Config.OtelVerbose && turnSpan != nil {
		names := make([]string, len(calls))
		for i, tc := range calls {
			names[i] = tc.Function.Name
		}
		turnSpan.AddEvent("dispatch.inline", trace.WithAttributes(
			attribute.Int("inline.count", len(calls)),
			attribute.String("inline.tool_names", strings.Join(names, ",")),
		))
	}

	toolResults := l.executeAndCollect(turnCtx, calls, messages)
	(*step).Messages = *messages

	if l.ConflictTracker != nil {
		l.Hooks.Emit(HookEvent{
			Event: ConflictCheck,
			Turn:  iter,
			Model: l.Config.Model,
		})

		if report := l.ConflictTracker.DetectAndReset(); report != "" {
			fileCount := strings.Count(report, "File: ")
			l.Hooks.Emit(HookEvent{
				Event:         ConflictDetect,
				Turn:          iter,
				Model:         l.Config.Model,
				ConflictFiles: fileCount,
			})
			if turnSpan != nil {
				turnSpan.SetAttributes(attribute.Int("conflict.files", fileCount))
				turnSpan.AddEvent("conflict.detected", trace.WithAttributes(
					attribute.Int("conflict.files", fileCount),
				))
			}
			conflictMsg := types.UserMsg(report)
			*messages = append(*messages, conflictMsg)
			(*step).Messages = *messages
			l.State.Messages = *messages
			l.Persister.Persist(conflictMsg)
		}
	}

	var err error
	*step, err = pipe.RunPostTool(turnCtx, toolResults, *step)
	if err != nil {
		if turnSpan != nil {
			turnSpan.End()
		}
		l.State.Messages = *messages
		return err
	}

	return nil
}

// injectWrapUpNotice appends a transient wrap-up notice to the request,
// warning the model that its iteration budget is nearly exhausted so it
// finishes and summarizes before tools are stripped or the run ends.
// The notice lives only in the request — it is never persisted to the
// conversation history, and the countdown updates on each iteration.
func (l *Loop) injectWrapUpNotice(req *types.ChatRequest, turnSpan trace.Span, remaining int) {
	req.Messages = append(req.Messages, types.UserMsg(prompts.WrapUpMessage(remaining)))
	if l.Config.OtelEnabled && turnSpan != nil {
		turnSpan.AddEvent("maxturns.wrap_up", trace.WithAttributes(
			attribute.Int("maxturns.remaining", remaining),
		))
	}
}

// ctxMgr returns the CtxMgr, creating one lazily if needed.
// This avoids nil panics when tests call context methods directly
// without going through Run → applyDefaults.
func (l *Loop) ctxMgr() *ContextManager {
	if l.CtxMgr == nil {
		l.CtxMgr = &ContextManager{}
	}
	return l.CtxMgr
}

// applyDefaults sets default values for Loop fields.
func (l *Loop) applyDefaults() {
	if l.CtxMgr == nil {
		l.CtxMgr = NewContextManager(l.Provider, l.Config.Model)
		l.CtxMgr.ContextWindow = l.Config.ContextWindow
		l.CtxMgr.CompactionThreshold = l.Config.CompactionThreshold
		l.CtxMgr.RawCompactionThreshold = l.Config.RawCompactionThreshold
		l.CtxMgr.EstimateFactor = l.Config.EstimateFactor
		l.CtxMgr.PreviousSummary = l.State.PreviousSummary
		l.CtxMgr.LastPromptTokens = l.State.LastPromptTokens
		l.CtxMgr.LastCachedPromptTokens = l.State.LastCachedPromptTokens
		l.CtxMgr.LastCompactionTokens = l.State.LastCompactionTokens
		l.CtxMgr.IneffectiveCompactions = l.State.IneffectiveCompactions
		l.CtxMgr.CompactProvider = l.CompactProvider
		l.CtxMgr.CompactModel = l.Config.CompactModel
		l.CtxMgr.SessionID = l.Config.SessionID
		l.CtxMgr.OtelEnabled = l.Config.OtelEnabled
	}
	if l.CtxMgr.ReasoningProtectTurns <= 0 {
		l.CtxMgr.ReasoningProtectTurns = 2
	}
	l.CtxMgr.EnsurePruner()
	if l.State.CompactionBudgetMultiplier <= 0 {
		l.State.CompactionBudgetMultiplier = 1.0
	}
	if l.Config.MaxLoopCycles <= 0 {
		l.Config.MaxLoopCycles = 50
	}
	if l.Config.WrapUpThreshold == 0 {
		l.Config.WrapUpThreshold = 1
	}
	if l.Config.Model == "" {
		l.Config.Model = "deepseek-v4-pro"
	}
	if l.Config.RetryBackoff <= 0 {
		l.Config.RetryBackoff = time.Second
	}
	if l.Config.LoopDetectCount <= 0 {
		l.Config.LoopDetectCount = 5
	}
	if l.Config.LoopDetectWindow <= 0 {
		l.Config.LoopDetectWindow = 10
	}
	if l.Config.MaxToolConcurrency > 0 && l.toolConcurrency == nil {
		l.toolConcurrency = pipeline.NewToolConcurrencyMiddleware(l.Config.MaxToolConcurrency)
	}
	if l.Config.MaxSubAgentConcurrency > 0 && l.subAgentSem == nil {
		l.subAgentSem = make(chan struct{}, l.Config.MaxSubAgentConcurrency)
	}
	if l.Hooks == nil {
		l.Hooks = NewHookEmitter("", l.Config.SessionID)
	}
	if l.Persister == nil {
		l.Persister = NewSessionPersister(nil, nil, l.Config.SessionID)
	}
	if l.LLM == nil {
		if l.View != nil {
			l.broker = pubsub.NewBroker[Event]()
			l.brokerView = NewBrokerView(l.broker, l.View)
		}
		var onToken llm.TokenCallback
		var onThinking llm.ThinkingCallback
		if l.broker != nil {
			onToken = func(token string) {
				l.broker.Publish(&TokenDeltaEvent{Text: token})
			}
			onThinking = func(text string) {
				l.broker.Publish(&ThinkingEvent{Text: text})
			}
		}
		l.LLM = &llm.Client{
			Provider:         l.Provider,
			FallbackProvider: l.FallbackProvider,
			Model:            l.Config.Model,
			FallbackModel:    l.FallbackModel,
			MaxRetries:       l.Config.MaxRetries,
			RetryBackoff:     l.Config.RetryBackoff,
			ContextWindow:    l.Config.ContextWindow,
			SessionID:        l.Config.SessionID,
			OnToken:          onToken,
			OnThinking:       onThinking,
			Compact:          l.llmCompact,
			Trim:             l.llmTrim,
			StripReasoning:   l.StripAllReasoning,
			OtelEnabled:      l.Config.OtelEnabled,
			OtelVerbose:      l.Config.OtelVerbose,
		}
	}
}

func (l *Loop) buildToolsForLevel() []types.ToolDef {
	switch l.Config.ToolsLevel {
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
	l.State.TotalTokens.PromptTokens += u.PromptTokens
	l.State.TotalTokens.CompletionTokens += u.CompletionTokens
	l.State.TotalTokens.TotalTokens += u.TotalTokens
	l.State.LastPromptTokens = u.PromptTokens
	if d := u.CompletionTokensDetails; d != nil {
		l.State.TotalReasoningTokens += d.ReasoningTokens
	}
	if d := u.PromptTokensDetails; d != nil {
		l.State.TotalCachedPromptTokens += d.CachedTokens
		l.State.LastCachedPromptTokens = d.CachedTokens
	} else {
		l.State.LastCachedPromptTokens = 0
	}
}

func (l *Loop) llmCompact(ctx context.Context, messages []types.Message, threshold float64) []types.Message {
	l.State.Messages = messages
	l.compactContext(ctx, threshold)
	return l.State.Messages
}

// llmTrim reduces context deterministically by removing the oldest
// messages. It is used as a fallback when the LLM returns an empty
// stream, indicating the context is too large even for summarization.
func (l *Loop) llmTrim(ctx context.Context, messages []types.Message) []types.Message {
	l.State.Messages = messages
	l.trimContext()
	return l.State.Messages
}
