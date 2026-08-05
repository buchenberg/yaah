package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/buchenberg/yaah/internal/agent/llm"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/pubsub"
	"github.com/buchenberg/yaah/internal/types"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// ContextManager owns context-window policy configuration and state:
// compaction thresholds, pruning tuning, token tracking, and truncation
// limits. Extracted from the Loop to isolate context management concerns
// and make them independently configurable and testable.
//
// Phase 1: holds config and state fields. Methods still live on Loop
// (agent_context.go, agent_truncation.go, agent_reasoning.go) and read
// from this struct. Phase 2 will migrate the methods here.
type ContextManager struct {
	// Context window and compaction thresholds.
	ContextWindow          int
	CompactionThreshold    float64
	RawCompactionThreshold float64
	EstimateFactor         float64

	// Reasoning and truncation tuning.
	ReasoningProtectTurns int
	ToolResultMaxLines    int
	ToolResultMaxBytes    int

	// Pruner and its tuning knobs.
	Pruner             *pipeline.Pruner
	PruneProtectTokens int
	PruneMinReclaim    int
	PruneMinTurns      int

	// Compaction state.
	PreviousSummary          string
	LastPromptTokens         int
	LastCachedPromptTokens   int
	LastCompactionTokens     int
	IneffectiveCompactions   int
	CompactionSavingsHistory []float64

	// Injected dependencies for compaction LLM calls.
	Provider        Provider
	Model           string
	SystemPrompt    string
	CompactProvider Provider
	CompactModel    string
	DB              *memory.DB
	SessionID       string
	OtelEnabled     bool

	// --- Phase 2: compaction infrastructure ---

	// LLMClient allows ContextManager to call LLMs for summarisation.
	LLMClient *llm.Client

	// CompactMaxMessages is the max messages to include in a compaction
	// summary from the tail.
	CompactMaxMessages int

	// CompactionBudgetMultiplier grows when back-to-back overflows occur.
	CompactionBudgetMultiplier float64

	// CompactionForcedByOverflow is set when a forced-compaction due to
	// context overflow occurred this turn.
	CompactionForcedByOverflow bool

	// Tracer for OpenTelemetry spans during compaction.
	Tracer trace.Tracer

	// Broker for publishing compaction lifecycle events.
	Broker *pubsub.Broker[Event]

	// CompactionHook is an optional callback invoked during compaction.
	CompactionHook func(event any)

	// Messages is a mutable snapshot of the current message list, used by
	// compactFn to read/write the working set.
	Messages []types.Message

	// compactFn is set by the Loop to delegate compaction back through its
	// own method while ContextManager satisfies the Compactor interface.
	compactFn func(ctx context.Context, messages []types.Message, threshold float64) []types.Message
}

// Reset resets all compaction-tracking state to zero values.
func (cm *ContextManager) Reset() {
	cm.PreviousSummary = ""
	cm.LastPromptTokens = 0
	cm.LastCachedPromptTokens = 0
	cm.LastCompactionTokens = 0
	cm.IneffectiveCompactions = 0
	cm.CompactionSavingsHistory = nil
	cm.CompactionBudgetMultiplier = 1.0
	cm.CompactionForcedByOverflow = false
}

// Compact implements pipeline.Compactor by delegating to the registered
// compaction function.
func (cm *ContextManager) Compact(ctx context.Context, messages []types.Message, threshold float64) []types.Message {
	if cm.compactFn == nil {
		return messages
	}
	return cm.compactFn(ctx, messages, threshold)
}

// NewContextManager creates a ContextManager with the given dependencies.
// Tuning fields are zero-valued; callers set them from config.
func NewContextManager(provider Provider, model string) *ContextManager {
	return &ContextManager{
		Provider: provider,
		Model:    model,
	}
}

// EnsurePruner constructs a default Pruner when none is attached,
// applying any tuning overrides from the config fields.
func (cm *ContextManager) EnsurePruner() {
	if cm.Pruner == nil {
		cfg := pipeline.DefaultPruneConfig()
		if cm.PruneProtectTokens > 0 {
			cfg.ProtectTokens = cm.PruneProtectTokens
		}
		if cm.PruneMinReclaim > 0 {
			cfg.MinReclaim = cm.PruneMinReclaim
		}
		if cm.PruneMinTurns > 0 {
			cfg.MinTurns = cm.PruneMinTurns
		}
		cm.Pruner = pipeline.NewPruner(cfg)
	}
}

// estimatedTokens returns the char/4 token estimate for all messages.
func (cm *ContextManager) estimatedTokens() int {
	total := 0
	for _, m := range cm.Messages {
		total += messageTokens(m)
	}
	return total
}

// resetPruner clears the pruner's marked set and re-evaluates from scratch.
func (cm *ContextManager) resetPruner() {
	if cm.Pruner != nil {
		cm.Pruner.Reset()
	}
}

// persisterDB returns the underlying database from the ContextManager, or nil
// when DB hasn't been set.
func (cm *ContextManager) persisterDB() *memory.DB {
	return cm.DB
}

// applyCompactedSummary replaces the message list with the compacted summary
// plus kept messages. It is called by both the normal and chunked compaction
// paths so they share the same post-compaction logic.
func (cm *ContextManager) applyCompactedSummary(summary string, sysMsg types.Message, oldMsgs, keepMsgs []types.Message, preRealTokens int) {
	cm.PreviousSummary = summary

	newMsgs := []types.Message{sysMsg}
	if cm.SystemPrompt == "" {
		newMsgs[0] = types.SystemMsg(summary)
	} else {
		newMsgs = append(newMsgs, types.SystemMsg(
			"You are continuing an ongoing conversation. Below is a summary of earlier discussion. Continue naturally — do not greet, reintroduce yourself, or act like the conversation is starting over.\n\nPrevious conversation summary:\n"+summary))
	}

	if lastUser := lastUserPrompt(oldMsgs); lastUser != "" {
		alreadyKept := false
		for _, m := range keepMsgs {
			if m.Role == "user" && m.Content == lastUser {
				alreadyKept = true
				break
			}
		}
		if !alreadyKept {
			newMsgs = append(newMsgs, types.SystemMsg("Active task (preserve verbatim):\n"+lastUser))
		}
	}

	newMsgs = append(newMsgs, keepMsgs...)
	beforeEstimate := cm.estimatedTokens()
	cm.Messages = newMsgs
	cm.LastPromptTokens = cm.estimatedTokens()
	cm.resetPruner()
	if cm.Pruner != nil {
		cm.Pruner.Mark(cm.Messages, "post_compaction")
	}

	afterEstimate := cm.estimatedTokens()

	minReduction := cm.ContextWindow / 20
	if minReduction < 2000 {
		minReduction = 2000
	}
	if beforeEstimate-afterEstimate < minReduction {
		cm.IneffectiveCompactions++
	} else {
		cm.IneffectiveCompactions = 0
	}
	cm.trackCompactionSavings(float64(beforeEstimate-afterEstimate) / float64(beforeEstimate))

	cm.LastCompactionTokens = afterEstimate
	if preRealTokens > cm.LastCompactionTokens {
		cm.LastCompactionTokens = preRealTokens
	}
}

const adaptiveSavingsWindow = 5

func (cm *ContextManager) trackCompactionSavings(savings float64) {
	cm.CompactionSavingsHistory = append(cm.CompactionSavingsHistory, savings)
	if len(cm.CompactionSavingsHistory) > adaptiveSavingsWindow {
		cm.CompactionSavingsHistory = cm.CompactionSavingsHistory[1:]
	}

	highCount := 0
	lowCount := 0
	for _, s := range cm.CompactionSavingsHistory {
		if s > 0.4 {
			highCount++
		}
		if s < 0.1 {
			lowCount++
		}
	}

	if highCount >= 3 {
		cm.CompactionBudgetMultiplier *= 0.9
		cm.CompactionSavingsHistory = nil
	}
	if lowCount >= 2 {
		cm.CompactionBudgetMultiplier *= 1.2
		cm.CompactionSavingsHistory = nil
	}
	if cm.CompactionBudgetMultiplier < 0.5 {
		cm.CompactionBudgetMultiplier = 0.5
	}
	if cm.CompactionBudgetMultiplier > 2.0 {
		cm.CompactionBudgetMultiplier = 2.0
	}
}

// compactContext checks if the estimated token count exceeds the given
// fraction of ContextWindow. If threshold is 0, defaults to 0.25 (25%).
// If over budget, it uses the LLM to summarize old messages into a
// structured summary, preserving the system message and recent turns.
// Falls back to simple trimming if the LLM call fails or returns empty.
func (cm *ContextManager) compactContext(ctx context.Context, threshold float64) {
	if cm.IneffectiveCompactions >= 2 && cm.LastCompactionTokens > 0 {
		if est := cm.estimatedTokens(); est >= cm.LastCompactionTokens*3/2 {
			cm.IneffectiveCompactions = 0
			if db := cm.persisterDB(); cm.SessionID != "" && db != nil {
				db.SetCompactionCooldown(cm.SessionID, 0, 0)
			}
		}
	}

	if cm.IneffectiveCompactions >= 2 {
		return
	}

	if db := cm.persisterDB(); cm.SessionID != "" && db != nil {
		cooldown, ineffective, err := db.GetCompactionCooldown(cm.SessionID)
		if err == nil && cooldown > 0 && time.Now().Unix() < cooldown {
			return
		}
		if err == nil && ineffective != cm.IneffectiveCompactions {
			cm.IneffectiveCompactions = ineffective
		}
	}

	if threshold <= 0 {
		threshold = 0.25
	}

	target := int(float64(cm.ContextWindow) * threshold)
	if target < minContextFloor && cm.ContextWindow >= minContextFloor {
		target = minContextFloor
	}

	rawTokens := cm.LastPromptTokens
	effectiveTokens := rawTokens
	if effectiveTokens > 0 && cm.LastCachedPromptTokens > 0 {
		effectiveTokens -= cm.LastCachedPromptTokens
	}
	if effectiveTokens <= 0 {
		factor := cm.EstimateFactor
		if factor <= 0 {
			factor = defaultEstimateFactor
		}
		effectiveTokens = preflightTokens(cm.Messages, nil, factor)
		rawTokens = effectiveTokens
	}

	rawThreshold := cm.RawCompactionThreshold
	if rawThreshold <= 0 {
		rawThreshold = defaultRawCompactionThreshold
	}
	rawTarget := int(float64(cm.ContextWindow) * rawThreshold)

	if effectiveTokens < target && rawTokens < rawTarget {
		if cm.CompactMaxMessages <= 0 || len(cm.Messages) <= cm.CompactMaxMessages {
			return
		}
	}

	if len(cm.Messages) <= 4 {
		return
	}

	compactReason := "threshold"
	if cm.CompactionForcedByOverflow {
		compactReason = "overflow"
		cm.CompactionForcedByOverflow = false
	} else if effectiveTokens < target && rawTokens < rawTarget {
		compactReason = "message_count"
	}

	if cm.Broker != nil {
		cm.Broker.Publish(&CompactionStartedEvent{
			BeforeTokens: rawTokens,
			TargetTokens: target,
			Reason:       compactReason,
		})
	}

	startTime := time.Now()

	if cm.OtelEnabled {
		_, span := otel.Tracer("yaah").Start(ctx, "compaction",
			trace.WithAttributes(
				attribute.Int("compaction.effective_tokens", effectiveTokens),
				attribute.Int("compaction.raw_tokens", rawTokens),
				attribute.Int("compaction.cached_tokens", cm.LastCachedPromptTokens),
				attribute.Int("compaction.target", target),
				attribute.Int("compaction.raw_target", rawTarget),
				attribute.Int("compaction.messages", len(cm.Messages)),
			))
		defer span.End()
	}

	sysMsg := cm.Messages[0]

	budget := int(float64(preserveBudget(cm.ContextWindow))*cm.CompactionBudgetMultiplier) / 4
	split := splitTail(cm.Messages, budget)
	keepMsgs := cm.Messages[split.keepStart:]
	oldMsgs := cm.Messages[1:split.keepStart]

	if cm.ReasoningProtectTurns > 0 {
		split.keepStart = ProtectReasoningTurns(cm.Messages, split.keepStart, cm.ReasoningProtectTurns)
		keepMsgs = cm.Messages[split.keepStart:]
		oldMsgs = cm.Messages[1:split.keepStart]
	}
	oldMsgs = pruneMessages(oldMsgs, pruneMessageMaxLen)

	var sb strings.Builder
	if cm.PreviousSummary != "" {
		sb.WriteString("Update the anchored summary below using the conversation history above.\n")
		sb.WriteString("Preserve still-true details, remove stale details, and merge in the new facts.\n")
		sb.WriteString("<previous-summary>\n")
		sb.WriteString(cm.PreviousSummary)
		sb.WriteString("\n</previous-summary>\n\n")
	} else {
		sb.WriteString("Create a new anchored summary from the conversation history below.\n\n")
	}
	sb.WriteString("Conversation excerpt to summarize:\n\n")
	for _, m := range oldMsgs {
		if m.Content != "" {
			if m.Role == "tool" {
				sb.WriteString(formatToolStub(m) + "\n")
			} else {
				sb.WriteString(fmt.Sprintf("%s: %s\n", m.Role, m.Content))
			}
		}
		for _, tc := range m.ToolCalls {
			sb.WriteString(fmt.Sprintf("[tool:%s] %s\n", tc.Function.Name, tc.Function.Arguments))
		}
	}
	sb.WriteString("\n\n")
	sb.WriteString(summaryTemplate)

	compactProvider := cm.CompactProvider
	if compactProvider == nil {
		compactProvider = cm.Provider
	}
	compactModel := cm.CompactModel
	if compactModel == "" {
		compactModel = cm.Model
	}

	req := types.ChatRequest{
		Model:     compactModel,
		MaxTokens: 4096,
		Messages: []types.Message{
			types.UserMsg(sb.String()),
		},
	}

	beforeEstimate := cm.estimatedTokens()
	resp, err := compactProvider.Send(ctx, req)
	if err != nil || len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		if len(oldMsgs) > minChunkTokens {
			if chunkSummary, chunkErr := cm.chunkedCompact(ctx, oldMsgs, compactModel); chunkErr == nil && chunkSummary != "" {
				cm.applyCompactedSummary(chunkSummary, sysMsg, oldMsgs, keepMsgs, effectiveTokens)
				afterEstimate := cm.estimatedTokens()
				savingsPct := 0.0
				if beforeEstimate > 0 {
					savingsPct = float64(beforeEstimate-afterEstimate) / float64(beforeEstimate)
				}
				observability.RecordCompaction(ctx, compactReason, time.Since(startTime), beforeEstimate, afterEstimate, savingsPct)
				return
			}
		}
		cm.trimContext()
		return
	}

	summary := resp.Choices[0].Message.Content
	cm.applyCompactedSummary(summary, sysMsg, oldMsgs, keepMsgs, effectiveTokens)
	afterEstimate := cm.estimatedTokens()
	savingsPct := 0.0
	ineffectiveNote := ""
	if beforeEstimate > 0 {
		savingsPct = float64(beforeEstimate-afterEstimate) / float64(beforeEstimate)
	}

	if cm.IneffectiveCompactions >= 2 {
		ineffectiveNote = fmt.Sprintf("compaction ineffective %d times; cooldown active", cm.IneffectiveCompactions)
	}

	if cm.Broker != nil {
		cm.Broker.Publish(&CompactionDoneEvent{
			BeforeTokens:    beforeEstimate,
			AfterTokens:     afterEstimate,
			SavingsPct:      savingsPct,
			Method:          "single",
			ElapsedSeconds:  time.Since(startTime).Seconds(),
			IneffectiveNote: ineffectiveNote,
			OldMsgCount:     len(oldMsgs),
			KeepMsgCount:    len(keepMsgs),
			Budget:          budget,
		})
	}

	observability.RecordCompaction(ctx, compactReason, time.Since(startTime), beforeEstimate, afterEstimate, savingsPct)

	if db := cm.persisterDB(); cm.SessionID != "" && db != nil {
		cooldown := int64(0)
		if cm.IneffectiveCompactions >= 2 {
			cooldown = time.Now().Unix() + 600
		}
		db.SetCompactionCooldown(cm.SessionID, cooldown, cm.IneffectiveCompactions)
		db.UpdateSessionSummary(cm.SessionID, summary)
	}
}

// trimContext removes old messages when the estimated token count exceeds
// 80% of ContextWindow. Preserves the system message and recent exchanges.
// Reasoning-carrying assistant messages are protected via ProtectReasoningTurns.
// This is a fallback when LLM-powered compaction is unavailable.
func (cm *ContextManager) trimContext() {
	target := cm.ContextWindow * 4 / 5
	totalChars := 0
	for _, m := range cm.Messages {
		totalChars += len(m.Content) + len(m.ReasoningContent)
		for _, tc := range m.ToolCalls {
			totalChars += len(tc.Function.Arguments) + len(tc.Function.Name)
		}
	}
	if totalChars/4 <= target {
		return
	}

	sysMsg := cm.Messages[0]
	rest := cm.Messages[1:]
	for len(rest) > 0 && totalChars/4 > target {
		removed := len(rest[0].Content) + len(rest[0].ReasoningContent)
		for _, tc := range rest[0].ToolCalls {
			removed += len(tc.Function.Arguments) + len(tc.Function.Name)
		}
		totalChars -= removed
		rest = rest[1:]
	}

	keepStart := len(cm.Messages) - len(rest)
	if cm.ReasoningProtectTurns > 0 {
		keepStart = ProtectReasoningTurns(cm.Messages, keepStart, cm.ReasoningProtectTurns)
	}

	newMsgs := make([]types.Message, 0, len(cm.Messages)-keepStart+1)
	newMsgs = append(newMsgs, sysMsg)
	newMsgs = append(newMsgs, cm.Messages[keepStart:]...)
	cm.Messages = newMsgs
	cm.resetPruner()
	if cm.Pruner != nil {
		cm.Pruner.Mark(cm.Messages, "post_trim")
	}
}

// summarizeChunk sends a single chunk to the compact model for summarization.
func (cm *ContextManager) summarizeChunk(ctx context.Context, chunk []types.Message, chunkIdx, total int) (string, error) {
	if len(chunk) == 0 {
		return "", nil
	}

	provider := cm.CompactProvider
	if provider == nil {
		provider = cm.Provider
	}
	model := cm.CompactModel
	if model == "" {
		model = cm.Model
	}

	var sb strings.Builder
	sb.WriteString(prompts.ChunkPreamble(chunkIdx+1, total) + "\n\n")
	sb.WriteString("<conversation>\n")
	for _, m := range chunk {
		if m.Content != "" {
			if m.Role == "tool" {
				sb.WriteString(formatToolStub(m) + "\n")
			} else {
				sb.WriteString(fmt.Sprintf("%s: %s\n", m.Role, m.Content))
			}
		}
		for _, tc := range m.ToolCalls {
			sb.WriteString(fmt.Sprintf("[tool_call:%s] %s\n", tc.Function.Name, tc.Function.Arguments))
		}
	}
	sb.WriteString("</conversation>\n\n")
	sb.WriteString(prompts.SummaryTemplate())

	req := types.ChatRequest{
		Model:     model,
		MaxTokens: 4096,
		Messages: []types.Message{
			types.SystemMsg(prompts.ChunkSummarizerRole()),
			types.UserMsg(sb.String()),
		},
	}

	resp, err := provider.Send(ctx, req)
	if err != nil {
		return "", fmt.Errorf("chunk %d/%d summarization failed: %w", chunkIdx+1, total, err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		return "", nil
	}
	return resp.Choices[0].Message.Content, nil
}

// chunkedCompact performs multi-pass chunked summarization of old messages.
func (cm *ContextManager) chunkedCompact(ctx context.Context, oldMsgs []types.Message, compactModel string) (string, error) {
	if len(oldMsgs) == 0 {
		return "", nil
	}

	chunkBudget := int(float64(cm.ContextWindow) * chunkBudgetFraction)
	if chunkBudget < minChunkTokens {
		chunkBudget = minChunkTokens
	}

	chunks := chunkSplit(oldMsgs, chunkBudget)
	if len(chunks) <= 1 {
		return cm.summarizeChunk(ctx, oldMsgs, 0, 1)
	}

	sem := make(chan struct{}, maxChunkConcurrency)
	var wg sync.WaitGroup
	results := make([]string, len(chunks))
	errors := make([]error, len(chunks))

	for i, chunk := range chunks {
		wg.Add(1)
		go func(idx int, c []types.Message) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[idx], errors[idx] = cm.summarizeChunk(ctx, c, idx, len(chunks))
		}(i, chunk)
	}
	wg.Wait()

	var partials []string
	for i, r := range results {
		if errors[i] != nil {
			continue
		}
		if strings.TrimSpace(r) != "" {
			partials = append(partials, r)
		}
	}
	if len(partials) == 0 {
		return "", fmt.Errorf("all chunk summarizations failed")
	}
	if len(partials) == 1 {
		return partials[0], nil
	}
	return cm.reducePartialSummaries(ctx, partials, 1, compactModel)
}

// reducePartialSummaries recursively merges partial summaries into one.
func (cm *ContextManager) reducePartialSummaries(ctx context.Context, partials []string, depth int, compactModel string) (string, error) {
	if len(partials) == 1 {
		return partials[0], nil
	}
	if len(partials) == 0 {
		return "", nil
	}
	if depth > maxReduceDepth {
		return strings.Join(partials, "\n###\n"), nil
	}

	provider := cm.CompactProvider
	if provider == nil {
		provider = cm.Provider
	}
	model := cm.CompactModel
	if model == "" {
		model = cm.Model
	}

	var sb strings.Builder
	sb.WriteString(prompts.ChunkMergerPreamble() + "\n\n")
	for i, p := range partials {
		sb.WriteString(fmt.Sprintf("<partial-summary-%d>\n%s\n</partial-summary-%d>\n\n", i+1, p, i+1))
	}
	sb.WriteString(prompts.SummaryTemplate())

	req := types.ChatRequest{
		Model:     model,
		MaxTokens: 4096,
		Messages: []types.Message{
			types.SystemMsg(prompts.ChunkMergerRole()),
			types.UserMsg(sb.String()),
		},
	}

	resp, err := provider.Send(ctx, req)
	if err != nil {
		return strings.Join(partials, "\n###\n"), nil
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		return strings.Join(partials, "\n###\n"), nil
	}

	merged := resp.Choices[0].Message.Content
	combinedLen := 0
	for _, p := range partials {
		combinedLen += len(p)
	}
	if float64(len(merged)) > float64(combinedLen)*0.8 {
		mid := len(partials) / 2
		left, err := cm.reducePartialSummaries(ctx, partials[:mid], depth+1, compactModel)
		if err != nil {
			return merged, nil
		}
		right, err := cm.reducePartialSummaries(ctx, partials[mid:], depth+1, compactModel)
		if err != nil {
			return merged, nil
		}
		return cm.reducePartialSummaries(ctx, []string{left, right}, depth+1, compactModel)
	}
	return merged, nil
}
