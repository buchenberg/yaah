package agent

import (
	"context"
	"log/slog"

	agentctx "github.com/buchenberg/yaah/internal/agent/context"
	"github.com/buchenberg/yaah/internal/types"
)

// Re-exports for backward compatibility within the agent package (tests
// reference the historical unexported names).
const (
	defaultEstimateFactor = agentctx.DefaultEstimateFactor
	maxPayloadBytes       = agentctx.MaxPayloadBytes
)

type turnRange = agentctx.TurnRange

var (
	messageTokens        = agentctx.MessageTokens
	preflightTokens      = agentctx.PreflightTokens
	estimatePayloadBytes = agentctx.EstimatePayloadBytes
	turns                = agentctx.Turns
	preserveBudget       = agentctx.PreserveBudget
	splitTail            = agentctx.SplitTail
	splitTurn            = agentctx.SplitTurn
)

// ProtectReasoningTurns ensures compaction does not remove assistant messages
// that carry reasoning_content. Thinking-mode providers (e.g. DeepSeek) require
// EVERY reasoning-carrying assistant message to be passed back in every
// subsequent request. If compaction removes any, the next request gets a 400:
// "The reasoning_content in the thinking mode must be passed back to the API."
//
// protectTurns=0 disables protection entirely (explicit opt-out). Otherwise
// ALL reasoning-carrying messages in oldMsgs are protected, regardless of
// the configured count — DeepSeek requires every one.
func ProtectReasoningTurns(messages []types.Message, keepStart, protectTurns int) int {
	return agentctx.ProtectReasoningTurns(messages, keepStart, protectTurns)
}

// EstimatedTokens returns the estimated token count for all messages
// in the Loop's conversation history (l.State.Messages). This is the
// canonical estimate used by lifecycle events and control messages.
// Compacted methods should use cm.estimatedTokens() which reads the
// ContextManager's Messages snapshot.
func (l *Loop) EstimatedTokens() int {
	total := 0
	for _, m := range l.State.Messages {
		total += agentctx.MessageTokens(m)
	}
	return total
}

// prepareRequestMessages builds the ephemeral message slice sent to the
// provider for a single turn. It chains the request-time transformations that
// must NOT mutate the stored conversation history (l.State.Messages):
//
//  1. repairOrphans — drop orphaned tool results, synthesize results for
//     interrupted tool calls (allocates a fresh slice).
//  2. applyPruning  — stub soft-pruned tool results (Tier-0 context reclaim).
//
// ReasoningContent is NOT stripped: thinking-mode providers (e.g. DeepSeek)
// require it to be passed back in every assistant message. Stripping it
// triggers a 400: "The reasoning_content in the thinking mode must be passed
// back to the API."
//
// Because repairOrphans always returns a new slice, the stored history is never
// mutated by any of these passes.
func (l *Loop) prepareRequestMessages(messages []types.Message) []types.Message {
	out := l.applyPruning(repairOrphans(messages))

	// Guard: verify no reasoning-carrying assistant messages were lost.
	// The compaction/trim pipeline must either preserve every reasoning
	// message or fold its content into a system message summary.
	if src := agentctx.CountReasoningMessages(messages); agentctx.CountReasoningMessages(out) < src {
		slog.Error("reasoning_content lost — compaction or trim pipeline may have dropped reasoning-carrying messages",
			"source_count", agentctx.CountReasoningMessages(messages),
			"output_count", agentctx.CountReasoningMessages(out),
		)
	}

	return out
}

// StripAllReasoning permanently removes ReasoningContent from every assistant
// message in l.State.Messages. Called when a thinking-mode provider returns the
// "reasoning_content must be passed back" 400 error — the session is no longer
// in a valid thinking state and must continue without reasoning.
func (l *Loop) StripAllReasoning() {
	for i := range l.State.Messages {
		if l.State.Messages[i].Role == "assistant" {
			l.State.Messages[i].ReasoningContent = ""
		}
	}
}

// compactContext checks if the estimated token count exceeds the given
// fraction of ContextWindow. If threshold is 0, defaults to 0.25 (25%).
// If over budget, it uses the LLM to summarize old messages into a
// structured summary, preserving the system message and recent turns.
// Falls back to simple trimming if the LLM call fails or returns empty.
//
// Preflight: when LastPromptTokens is 0 (first call), uses preflightTokens
// with the configurable EstimateFactor (default 1.3) to estimate tokens.
// Cache subtraction: when LastCachedPromptTokens > 0, it is subtracted from
// LastPromptTokens so heavily-cached conversations don't over-trigger
// compaction (cached tokens are effectively free at the provider).
func (l *Loop) compactContext(ctx context.Context, threshold float64) {
	l.ctxMgr().compactContext(ctx, threshold)
}

// ForceCompact runs a single compaction pass on the loop's current messages,
// bypassing cooldowns and threshold guards so the user's explicit :compact
// command always runs. It applies defaults first (without creating a broker
// or forwarder goroutine) and flushes the persister on completion.
//
// ForceCompact must NOT be called concurrently with Run on the same Loop.
// It temporarily sets l.View to nil so applyDefaults does not create a
// broker/forwarder goroutine; callers must use a loop that is not running.
//
// The threshold parameter is forwarded to compactContext; pass 0 for the
// default 25% threshold. Cooldowns and ineffective-compaction counters are
// cleared before compaction so the guard at the top of compactContext does
// not skip the pass.
func (l *Loop) ForceCompact(ctx context.Context, threshold float64) {
	// Temporarily nil out View so applyDefaults does not create a broker
	// and forwarder goroutine — we never call Run(), so teardown would
	// never close them, causing a goroutine leak.
	savedView := l.View
	l.View = nil
	l.applyDefaults()
	l.View = savedView

	// Clear cooldown and ineffective-compaction state so the explicit
	// user request is not skipped by guards designed for automatic
	// in-loop compaction.
	cm := l.ctxMgr()
	cm.State.IneffectiveCompactions = 0
	if cm.SessionID != "" && cm.DB != nil {
		if err := cm.DB.SetCompactionCooldown(cm.SessionID, 0, 0); err != nil {
			slog.Warn("ForceCompact: failed to clear compaction cooldown", "error", err)
		}
	}

	// Force the token estimate high enough to bypass the threshold gate.
	// compactContext checks effectiveTokens >= ContextWindow * threshold;
	// setting LastPromptTokens to ContextWindow guarantees the gate fires.
	// Save and restore so subsequent automatic checks see the real estimate.
	savedLastPromptTokens := cm.State.LastPromptTokens
	cm.State.LastPromptTokens = cm.ContextWindow

	l.compactContext(ctx, threshold)

	// Restore the real token estimate so the next automatic compaction
	// check doesn't see an inflated value and compact without cause.
	cm.State.LastPromptTokens = savedLastPromptTokens

	// Flush any debounced writes from the persister so they are not lost.
	if l.Persister != nil {
		l.Persister.Flush()
	}
}

// trimContext removes old messages when the estimated token count exceeds
// 80% of ContextWindow. Preserves the system message and recent exchanges.
// Reasoning-carrying assistant messages are protected via ProtectReasoningTurns.
// This is a fallback when LLM-powered compaction is unavailable.
func (l *Loop) trimContext() {
	l.ctxMgr().trimContext()
}
