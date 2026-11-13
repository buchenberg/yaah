package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	agentctx "github.com/buchenberg/yaah/internal/agent/context"
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
// State points directly to the Loop's LoopState, so compaction methods
// read and write mutable state (Messages, PreviousSummary, token counts,
// compaction tracking) in place — no copy-in/copy-out sync dance.
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

	// State points to the Loop's LoopState, providing direct read/write
	// access to mutable compaction state without a sync dance.
	State *LoopState

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

	// Tracer for OpenTelemetry spans during compaction.
	Tracer trace.Tracer

	// Broker for publishing compaction lifecycle events.
	Broker *pubsub.Broker[Event]

	// CompactionHook is an optional callback invoked during compaction.
	CompactionHook func(event any)

	// OnMessagesReplaced, when non-nil, is invoked immediately after
	// compaction or trimming replaces State.Messages wholesale, with the
	// new slice. The Loop wires this to persistence rebasing so the DB
	// baseline tracks the post-compaction conversation (review B7).
	OnMessagesReplaced func(msgs []types.Message)

	// compactFn is set by the Loop to delegate compaction back through its
	// own method while ContextManager satisfies the Compactor interface.
	compactFn func(ctx context.Context, messages []types.Message, threshold float64) []types.Message
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
	for _, m := range cm.State.Messages {
		total += agentctx.MessageTokens(m)
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

// persistSessionSummary records the session row's compacted_summary. Both
// accepted paths — single-shot and chunked — record their summary here
// (yaah-l12q): a summary good enough to install is good enough to resume
// from, whichever path produced it. The trim fallback records a fallback
// marker instead — the rollover happened, and an empty row would hide it
// from resume (which injects the row verbatim) and from the scenario
// checkers.
func (cm *ContextManager) persistSessionSummary(summary string) {
	if db := cm.persisterDB(); cm.SessionID != "" && db != nil {
		db.UpdateSessionSummary(cm.SessionID, summary)
	}
}

// persistAcceptedCompaction is the shared tail of the two accepted
// compaction paths: the anti-thrash cooldown from the post-apply
// ineffective count, plus the summary record. Before yaah-l12q the
// chunked path skipped this tail entirely — it installed a real summary
// in the context but recorded nothing, so a restart resumed without it
// and the session row stayed empty while every rollover rode the
// fallback.
func (cm *ContextManager) persistAcceptedCompaction(summary string) {
	if db := cm.persisterDB(); cm.SessionID != "" && db != nil {
		cooldown := int64(0)
		if cm.State.IneffectiveCompactions >= 2 {
			cooldown = time.Now().Unix() + 600
		}
		db.SetCompactionCooldown(cm.SessionID, cooldown, cm.State.IneffectiveCompactions)
	}
	cm.persistSessionSummary(summary)
}

// applyCompactedSummary replaces the message list with the compacted summary
// plus kept messages. It is called by both the normal and chunked compaction
// paths so they share the same post-compaction logic.
func (cm *ContextManager) applyCompactedSummary(summary string, sysMsg types.Message, oldMsgs, keepMsgs []types.Message, preRealTokens int) {
	cm.State.PreviousSummary = summary

	newMsgs := []types.Message{sysMsg}
	if cm.SystemPrompt == "" {
		newMsgs[0] = types.SystemMsg(summary)
	} else {
		newMsgs = append(newMsgs, types.SystemMsg(agentctx.ContinuationNotice(summary)))
	}

	if lastUser := agentctx.ActiveTaskPrompt(oldMsgs); lastUser != "" {
		alreadyKept := false
		for _, m := range keepMsgs {
			if m.Role == "user" && m.Content == lastUser {
				alreadyKept = true
				break
			}
			// A prior rollover's carrier counts too — re-injecting a
			// task the tail already preserves duplicates it.
			if m.Role == "system" && m.Content == "Active task (preserve verbatim):\n"+lastUser {
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
	cm.State.Messages = newMsgs
	if cm.OnMessagesReplaced != nil {
		cm.OnMessagesReplaced(newMsgs)
	}
	estimate := cm.estimatedTokens()
	cm.State.LastPromptTokens = estimate
	cm.State.LastPromptTokensInclusive = estimate
	cm.State.LastPromptIncludesCache = false
	cm.State.LastCachedPromptTokens = 0
	cm.resetPruner()
	if cm.Pruner != nil {
		cm.Pruner.Mark(cm.State.Messages, "post_compaction")
	}

	afterEstimate := cm.estimatedTokens()

	minReduction := cm.ContextWindow / 20
	if minReduction < 2000 {
		minReduction = 2000
	}
	if beforeEstimate-afterEstimate < minReduction {
		cm.State.IneffectiveCompactions++
	} else {
		cm.State.IneffectiveCompactions = 0
	}
	cm.trackCompactionSavings(float64(beforeEstimate-afterEstimate) / float64(beforeEstimate))

	cm.State.LastCompactionTokens = afterEstimate
	if preRealTokens > cm.State.LastCompactionTokens {
		cm.State.LastCompactionTokens = preRealTokens
	}
}

const adaptiveSavingsWindow = 5

func (cm *ContextManager) trackCompactionSavings(savings float64) {
	cm.State.CompactionSavingsHistory = append(cm.State.CompactionSavingsHistory, savings)
	if len(cm.State.CompactionSavingsHistory) > adaptiveSavingsWindow {
		cm.State.CompactionSavingsHistory = cm.State.CompactionSavingsHistory[1:]
	}

	highCount := 0
	lowCount := 0
	for _, s := range cm.State.CompactionSavingsHistory {
		if s > 0.4 {
			highCount++
		}
		if s < 0.1 {
			lowCount++
		}
	}

	if highCount >= 3 {
		cm.State.CompactionBudgetMultiplier *= 0.9
		cm.State.CompactionSavingsHistory = nil
	}
	if lowCount >= 2 {
		cm.State.CompactionBudgetMultiplier *= 1.2
		cm.State.CompactionSavingsHistory = nil
	}
	if cm.State.CompactionBudgetMultiplier < 0.5 {
		cm.State.CompactionBudgetMultiplier = 0.5
	}
	if cm.State.CompactionBudgetMultiplier > 2.0 {
		cm.State.CompactionBudgetMultiplier = 2.0
	}
}

// compactContext checks if the estimated token count exceeds the given
// fraction of ContextWindow. If threshold is 0, defaults to 0.25 (25%).
// If over budget, it uses the LLM to summarize old messages into a
// structured summary, preserving the system message and recent turns.
// Falls back to simple trimming if the LLM call fails or returns empty.
func (cm *ContextManager) compactContext(ctx context.Context, threshold float64) {
	if cm.State.IneffectiveCompactions >= 2 && cm.State.LastCompactionTokens > 0 {
		if est := cm.estimatedTokens(); est >= cm.State.LastCompactionTokens*3/2 {
			cm.State.IneffectiveCompactions = 0
			if db := cm.persisterDB(); cm.SessionID != "" && db != nil {
				db.SetCompactionCooldown(cm.SessionID, 0, 0)
			}
		}
	}

	if cm.State.IneffectiveCompactions >= 2 {
		return
	}

	if db := cm.persisterDB(); cm.SessionID != "" && db != nil {
		cooldown, ineffective, err := db.GetCompactionCooldown(cm.SessionID)
		if err == nil && cooldown > 0 && time.Now().Unix() < cooldown {
			return
		}
		if err == nil && ineffective != cm.State.IneffectiveCompactions {
			cm.State.IneffectiveCompactions = ineffective
		}
	}

	if threshold <= 0 {
		threshold = 0.25
	}

	target := int(float64(cm.ContextWindow) * threshold)
	if target < agentctx.MinContextFloor && cm.ContextWindow >= agentctx.MinContextFloor {
		target = agentctx.MinContextFloor
	}

	rawTokens := cm.State.LastPromptTokens
	effectiveTokens := rawTokens
	if cm.State.LastPromptIncludesCache {
		// OpenAI shape: the raw prompt already contains cache reads.
		if effectiveTokens > 0 && cm.State.LastCachedPromptTokens > 0 {
			effectiveTokens -= cm.State.LastCachedPromptTokens
		}
	} else if rawTokens > 0 {
		// Anthropic shape: raw is uncached input (plus cache creation);
		// the context size for the raw gate is the inclusive total.
		rawTokens += cm.State.LastCachedPromptTokens
	}
	if effectiveTokens <= 0 {
		factor := cm.EstimateFactor
		if factor <= 0 {
			factor = agentctx.DefaultEstimateFactor
		}
		effectiveTokens = agentctx.PreflightTokens(cm.State.Messages, nil, factor)
		rawTokens = effectiveTokens
	}

	rawThreshold := cm.RawCompactionThreshold
	if rawThreshold <= 0 {
		rawThreshold = agentctx.DefaultRawCompactionThreshold
	}
	rawTarget := int(float64(cm.ContextWindow) * rawThreshold)

	if effectiveTokens < target && rawTokens < rawTarget {
		if cm.CompactMaxMessages <= 0 || len(cm.State.Messages) <= cm.CompactMaxMessages {
			return
		}
	}

	if len(cm.State.Messages) <= 4 {
		return
	}

	compactReason := "threshold"
	if cm.State.CompactionForcedByOverflow {
		compactReason = "overflow"
		cm.State.CompactionForcedByOverflow = false
	} else if effectiveTokens < target && rawTokens < rawTarget {
		compactReason = "message_count"
	}

	if cm.Broker != nil {
		// Compaction events are must-deliver (yaah-8jou.6): start and done
		// are a pair, and dropping either leaves a view with a
		// "compacting…" card that never closes, or one that never opened.
		// Both are rare — once per pass, a handful of passes per run — so
		// the bounded 50ms fan-out is affordable. Nothing is locked here:
		// the loop is a single writer and compaction runs on its goroutine.
		cm.Broker.PublishMustDeliver(&CompactionStartedEvent{
			BeforeTokens: rawTokens,
			TargetTokens: target,
			Reason:       compactReason,
		})
	}

	startTime := time.Now()

	// Reset the pass outcome: whichever path runs (or fails) records it,
	// so the manual :compact command renders what actually happened.
	cm.State.LastCompactionMethod = ""
	cm.State.LastCompactionSummary = ""

	var span trace.Span
	if cm.OtelEnabled {
		_, span = otel.Tracer("yaah").Start(ctx, "compaction",
			trace.WithAttributes(
				attribute.Int("compaction.effective_tokens", effectiveTokens),
				attribute.Int("compaction.raw_tokens", rawTokens),
				attribute.Int("compaction.cached_tokens", cm.State.LastCachedPromptTokens),
				attribute.Int("compaction.target", target),
				attribute.Int("compaction.raw_target", rawTarget),
				attribute.Int("compaction.messages", len(cm.State.Messages)),
			))
		defer span.End()
	}

	sysMsg := cm.State.Messages[0]

	// The preserved tail gets the full PreserveBudget (25% of the window,
	// clamped to [2K, 8K]). SplitTail only ever keeps whole turns, so the
	// clamp bounds the tail — no division here: the old /4 shrank the
	// verbatim tail to ~500-2000 tokens and pushed everything else
	// through the lossy summary.
	budget := int(float64(agentctx.PreserveBudget(cm.ContextWindow)) * cm.State.CompactionBudgetMultiplier)
	split := agentctx.SplitTail(cm.State.Messages, budget)
	keepMsgs := cm.State.Messages[split.KeepStart:]
	oldMsgs := cm.State.Messages[1:split.KeepStart]
	// The pre-protection boundary tells the two empty-oldMsgs shapes
	// apart (yaah-l12q): at <=1 the whole conversation fits the preserve
	// budget and the pass is structurally impossible (book skipped, do
	// not trim); above it, protection pinned the boundary to the system
	// message while the conversation is oversized — the pass is possible
	// only through the trim fallback.
	budgetKeepStart := split.KeepStart

	if cm.ReasoningProtectTurns > 0 {
		split.KeepStart = agentctx.ProtectReasoningTurns(cm.State.Messages, split.KeepStart, cm.ReasoningProtectTurns)
		keepMsgs = cm.State.Messages[split.KeepStart:]
		oldMsgs = cm.State.Messages[1:split.KeepStart]
	}
	oldMsgs = agentctx.PruneMessages(oldMsgs, agentctx.PruneMessageMaxLen)

	var sb strings.Builder
	if cm.State.PreviousSummary != "" {
		sb.WriteString("Update the anchored summary below using the conversation history above.\n")
		sb.WriteString("Preserve still-true details, remove stale details, and merge in the new facts.\n")
		sb.WriteString("<previous-summary>\n")
		sb.WriteString(cm.State.PreviousSummary)
		sb.WriteString("\n</previous-summary>\n\n")
	} else {
		sb.WriteString("Create a new anchored summary from the conversation history below.\n\n")
	}
	sb.WriteString("Conversation excerpt to summarize:\n\n")
	for _, m := range oldMsgs {
		if cm.State.PreviousSummary != "" && m.Role == "system" &&
			strings.Contains(m.Content, cm.State.PreviousSummary) {
			// The anchored summary already rides the <previous-summary>
			// section above; repeating it in the excerpt doubles the
			// prompt for no new information (yaah-an55). The carrier
			// holds the summary plus the continuation preamble and
			// nothing else of substance, so skipping it loses nothing.
			continue
		}
		if m.Content != "" {
			if m.Role == "tool" {
				sb.WriteString(agentctx.FormatToolStub(m) + "\n")
			} else {
				sb.WriteString(fmt.Sprintf("%s: %s\n", m.Role, m.Content))
			}
		}
		for _, tc := range m.ToolCalls {
			sb.WriteString(fmt.Sprintf("[tool:%s] %s\n", tc.Function.Name, excerptArgs(tc.Function.Arguments)))
		}
	}
	sb.WriteString("\n\n")
	sb.WriteString(agentctx.SummaryTemplate())

	compactProvider := cm.CompactProvider
	if compactProvider == nil {
		compactProvider = cm.Provider
	}
	compactModel := cm.CompactModel
	if compactModel == "" {
		compactModel = cm.Model
	}

	req := types.ChatRequest{
		Model: compactModel,
		Messages: []types.Message{
			types.UserMsg(sb.String()),
		},
	}

	beforeEstimate := cm.estimatedTokens()
	// Protection can pin SplitTail's boundary at the system message — a
	// single-turn session whose every reasoning carrier is "recent"
	// leaves nothing in oldMsgs. Asking the compact model to summarize
	// the bare template is a guaranteed placeholder and a wasted round
	// trip (yaah-l12q: the orch-compact shape paid eight of them). Say
	// so through the normal fallback reason instead.
	nothingToSummarize := len(oldMsgs) == 0
	var resp *types.ChatResponse
	var err error
	if !nothingToSummarize {
		resp, err = compactProvider.Send(ctx, req)
	}
	summaryRejected := ""
	if err == nil && resp != nil && len(resp.Choices) > 0 {
		if resp.Choices[0].FinishReason == "length" {
			// An output-cap-truncated summary silently drops its tail
			// sections; reject it into the chunked fallback rather than
			// install half a summary.
			summaryRejected = "summary truncated by output cap (finish_reason=length)"
			resp = nil
		}
	}
	if err == nil && resp != nil && len(resp.Choices) > 0 {
		if ok, vreason := agentctx.ValidateSummary(resp.Choices[0].Message.Content); !ok {
			// yaah-v378.1: a summary that says nothing — the unfilled
			// template is the recorded failure shape — must not replace
			// the conversation. Take the same fallback path as an empty
			// response, with the reason carried on the warn and the
			// event so the rejection is auditable.
			summaryRejected = vreason
			resp = nil
		}
	}
	if span != nil {
		accepted := err == nil && resp != nil && len(resp.Choices) > 0 &&
			resp.Choices[0].Message.Content != "" && summaryRejected == ""
		span.SetAttributes(
			attribute.Int("compaction.excerpt_bytes", sb.Len()),
			attribute.Bool("compaction.accepted", accepted),
		)
		if accepted {
			span.SetAttributes(attribute.String("compaction.summary_prefix",
				agentctx.BoundedSummaryPrefix(resp.Choices[0].Message.Content)))
		}
		if summaryRejected != "" {
			span.SetAttributes(attribute.String("compaction.reject_reason", summaryRejected))
		}
	}
	if err != nil || resp == nil || len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		reason := "empty response"
		switch {
		case nothingToSummarize:
			reason = "nothing to summarize outside the preserved tail"
		case err != nil:
			reason = err.Error()
		case summaryRejected != "":
			reason = "summary rejected: " + summaryRejected
		case resp != nil && len(resp.Choices) > 0 && resp.Choices[0].FinishReason != "":
			reason = "finish_reason=" + resp.Choices[0].FinishReason
		}
		if r := []rune(reason); len(r) > 256 {
			reason = string(r[:256]) + "..."
		}
		slog.Warn("compaction returned no summary; falling back to chunked or heuristic compaction",
			"session_id", cm.SessionID,
			"model", compactModel,
			"reason", reason,
		)
		factor := cm.EstimateFactor
		if factor <= 0 {
			factor = agentctx.DefaultEstimateFactor
		}
		if agentctx.PreflightTokens(oldMsgs, nil, factor) > agentctx.MinChunkTokens {
			chunkSummary, chunkErr := cm.chunkedCompact(ctx, oldMsgs, compactModel)
			if chunkErr == nil && chunkSummary != "" {
				if ok, vreason := agentctx.ValidateSummary(chunkSummary); ok {
					cm.applyCompactedSummary(chunkSummary, sysMsg, oldMsgs, keepMsgs, effectiveTokens)
					cm.State.LastCompactionMethod = "chunked"
					cm.State.LastCompactionSummary = chunkSummary
					// The summary was accepted, so record it like the
					// single path does (yaah-l12q): the orch-compact run's
					// every rollover rode this path while the session row
					// stayed empty.
					cm.persistAcceptedCompaction(chunkSummary)
					afterEstimate := cm.estimatedTokens()
					savingsPct := 0.0
					if beforeEstimate > 0 {
						savingsPct = float64(beforeEstimate-afterEstimate) / float64(beforeEstimate)
					}
					if cm.Broker != nil {
						cm.Broker.PublishMustDeliver(&CompactionDoneEvent{
							BeforeTokens:   beforeEstimate,
							AfterTokens:    afterEstimate,
							SavingsPct:     savingsPct,
							Method:         "chunked",
							ElapsedSeconds: time.Since(startTime).Seconds(),
							OldMsgCount:    len(oldMsgs),
							KeepMsgCount:   len(keepMsgs),
							Budget:         budget,
						})
					}
					observability.RecordCompaction(ctx, compactReason, time.Since(startTime), beforeEstimate, afterEstimate, savingsPct)
					return
				} else {
					// The chunked summarizer can echo the template too;
					// validating only the primary response would leave the
					// yaah-v378.1 shape reachable through this path.
					reason = "chunked summary rejected: " + vreason
					slog.Warn("compaction rejected the chunked summary; falling back to heuristic trim",
						"session_id", cm.SessionID, "reason", vreason)
				}
			} else {
				// Silent no more (yaah-l12q's residual): a chunked
				// error or an empty chunk summary dropped straight to
				// trim with no trace, which is how thirteen live
				// rollovers could ride a failing chunked path and
				// leave the diagnostic blind. Name what happened.
				slog.Warn("chunked compaction failed or returned nothing; falling back to heuristic trim",
					"session_id", cm.SessionID,
					"err", fmt.Sprint(chunkErr),
					"empty", chunkErr == nil)
			}
		}
		// The fits-the-tail shape (empty oldMsgs because the preserve
		// budget covers the whole conversation) is structurally impossible,
		// not a trim candidate (yaah-pcd2's kilo-review contract): the
		// count trigger included — the anti-thrash cooldown owns the
		// rearm story, and shrinking a conversation the tail already
		// holds destroys verbatim history for nothing. An overflow
		// pass is exempt: the provider itself rejected the context, so
		// the conversation genuinely does not fit regardless of yaah's
		// bookkeeping, and the trim is the only valve that shrinks the
		// message list enough for the retry.
		if nothingToSummarize && budgetKeepStart <= 1 && compactReason != "overflow" {
			cm.bookImpossibleCompaction(ctx, compactReason, startTime, len(keepMsgs), budget,
				"nothing to summarize — conversation fits the preserve budget")
			return
		}
		beforeTrim := len(cm.State.Messages)
		// The genuine task, not the newest synthetic row: after a steer
		// lands mid-loop, LastUserPrompt would preserve the churn notice
		// and every subsequent rollover would repeat it while the task
		// itself stayed lost — the live run answered instead of writing
		// the report (yaah-l12q's verify6 shape).
		lastUser := agentctx.ActiveTaskPrompt(cm.State.Messages)
		// The fallback must resolve what FIRED (yaah-l12q's residual):
		// a rollover that drops nothing neither records anything (the
		// marker below requires dropped messages) nor disarms its own
		// trigger, so it rearms on every subsequent message and pays
		// a full compaction attempt each time — the orch-compact
		// shape. trimContext's 80% window is not the line either
		// trigger measures against: the count trigger fires under both
		// token thresholds (so the token trim is a no-op by
		// construction), and the threshold trigger fires at
		// `target`, far below the 80% window (so the token trim stops
		// short of disarming it). Trim to each trigger's own line.
		// When even the target pass cuts nothing (reasoning protection
		// kept everything), the no-record rule still holds.
		if compactReason == "message_count" {
			cm.trimContext()
			cm.trimMessageBudget(cm.CompactMaxMessages)
		} else {
			cm.trimTokensTo(target)
		}
		cm.State.LastCompactionMethod = "trim"
		cm.State.LastCompactionSummary = ""
		// Record the rollover only when trim actually dropped messages
		// (yaah-l12q): a marker with nothing dropped would tell resume
		// (which injects the row as a system message) that context was
		// lost when none was. The in-memory summary stays empty either
		// way: trim installs none.
		if len(cm.State.Messages) < beforeTrim {
			// A trim that dropped the task's user prompt leaves the
			// agent with a preserved tail and no record of what it was
			// asked to do. The accepted path re-injects the live
			// instruction (applyCompactedSummary); the fallback must
			// too, or the first rollover mid-task is where the agent
			// forgets the deliverable.
			cm.reinjectActiveTask(lastUser)
			cm.persistSessionSummary(agentctx.FallbackMarker(reason))
		}
		if cm.Broker != nil {
			// Terminal event for the compaction card: the LLM
			// summarization fell back to plain old-message trimming,
			// which still compacts but does not summarize.
			afterTrim := cm.estimatedTokens()
			trimSavings := 0.0
			if beforeEstimate > 0 {
				trimSavings = float64(beforeEstimate-afterTrim) / float64(beforeEstimate)
			}
			cm.Broker.PublishMustDeliver(&CompactionDoneEvent{
				BeforeTokens:    beforeEstimate,
				AfterTokens:     afterTrim,
				SavingsPct:      trimSavings,
				Method:          "trim",
				ElapsedSeconds:  time.Since(startTime).Seconds(),
				IneffectiveNote: "summarization failed (" + reason + "); old messages trimmed",
				OldMsgCount:     len(oldMsgs),
				KeepMsgCount:    len(keepMsgs),
				Budget:          budget,
			})
		}
		return
	}

	summary := resp.Choices[0].Message.Content

	// The un-divided preserve budget (plus the message-count trigger, which
	// fires regardless of token gates) means the whole conversation can fit
	// in the tail: nothing was summarized, so installing would GROW the
	// history. Do not install; rebase the estimate (small windows rely on
	// it for the retry after overflow) and book the pass as ineffective so
	// the anti-thrash guard sees the no-op.
	if len(oldMsgs) == 0 {
		cm.bookImpossibleCompaction(ctx, compactReason, startTime, len(keepMsgs), budget,
			"nothing to summarize — conversation fits the preserve budget")
		return
	}

	cm.applyCompactedSummary(summary, sysMsg, oldMsgs, keepMsgs, effectiveTokens)
	cm.State.LastCompactionMethod = "single"
	cm.State.LastCompactionSummary = summary
	afterEstimate := cm.estimatedTokens()
	savingsPct := 0.0
	ineffectiveNote := ""
	if beforeEstimate > 0 {
		savingsPct = float64(beforeEstimate-afterEstimate) / float64(beforeEstimate)
	}

	if cm.State.IneffectiveCompactions >= 2 {
		ineffectiveNote = fmt.Sprintf("compaction ineffective %d times; cooldown active", cm.State.IneffectiveCompactions)
	}

	if cm.Broker != nil {
		cm.Broker.PublishMustDeliver(&CompactionDoneEvent{
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

	cm.persistAcceptedCompaction(summary)
}

// trimContext removes old messages when the estimated token count exceeds
// 80% of ContextWindow. Preserves the system message and recent exchanges.
// Reasoning-carrying assistant messages are protected via ProtectReasoningTurns.
// This is a fallback when LLM-powered compaction is unavailable.
func (cm *ContextManager) trimContext() {
	// The Loop's emergency valve (tools.go calls this when the provider
	// says the context cannot even be summarized). The compaction
	// fallback does not use this target — it trims to the compaction's
	// own target (trimTokensTo), the same line the trigger measures
	// against.
	cm.trimTokensTo(cm.ContextWindow * 4 / 5)
}

// trimTokensTo drops the oldest messages until the conversation fits
// the given token estimate — trimContext's loop, parameterized. The
// compaction fallback trims to the compaction target so the trigger
// that fired is actually resolved (yaah-l12q). Reasoning protection
// wins over the target, as everywhere else.
func (cm *ContextManager) trimTokensTo(target int) {
	if target <= 0 {
		return
	}
	totalChars := 0
	for _, m := range cm.State.Messages {
		totalChars += len(m.Content) + len(m.ReasoningContent)
		for _, tc := range m.ToolCalls {
			totalChars += len(tc.Function.Arguments) + len(tc.Function.Name)
		}
	}
	if totalChars/4 <= target {
		return
	}

	rest := cm.State.Messages[1:]
	for len(rest) > 0 && totalChars/4 > target {
		removed := len(rest[0].Content) + len(rest[0].ReasoningContent)
		for _, tc := range rest[0].ToolCalls {
			removed += len(tc.Function.Arguments) + len(tc.Function.Name)
		}
		totalChars -= removed
		rest = rest[1:]
	}

	keepStart := len(cm.State.Messages) - len(rest)
	// A trim that orphans a tool result from its assistant caller hands
	// the provider an invalid conversation (a hard 400). Snap to an
	// exchange boundary, then let the reasoning-protection window keep
	// the newest carriers (yaah-6a6c's windowed rule).
	keepStart = agentctx.SnapCutPastToolResults(cm.State.Messages, keepStart)
	if cm.ReasoningProtectTurns > 0 {
		keepStart = agentctx.ProtectReasoningTurns(cm.State.Messages, keepStart, cm.ReasoningProtectTurns)
	}
	cm.cutMessages(keepStart)
}

// cutMessages drops everything before keepStart, keeping the system
// message — the shared tail of trimContext and trimMessageBudget. The
// caller owns keepStart's contract (token window or message budget);
// reasoning protection, where it applies, was already folded in.
func (cm *ContextManager) cutMessages(keepStart int) {
	if keepStart < 1 {
		keepStart = 1 // never cut into the system message
	}
	if keepStart > len(cm.State.Messages) {
		keepStart = len(cm.State.Messages)
	}
	if len(cm.State.Messages) < 2 || keepStart <= 1 {
		return // nothing to cut; only the system message would remain
	}
	sysMsg := cm.State.Messages[0]
	newMsgs := make([]types.Message, 0, len(cm.State.Messages)-keepStart+1)
	newMsgs = append(newMsgs, sysMsg)
	newMsgs = append(newMsgs, cm.State.Messages[keepStart:]...)
	cm.State.Messages = newMsgs
	if cm.OnMessagesReplaced != nil {
		cm.OnMessagesReplaced(newMsgs)
	}
	cm.resetPruner()
	if cm.Pruner != nil {
		cm.Pruner.Mark(cm.State.Messages, "post_trim")
	}
}

// bookImpossibleCompaction closes a compaction pass that had nothing to
// summarize because the preserve tail already covers the whole
// conversation — reached from both the accepted path (a summary came back
// against an empty oldMsgs) and the fallback (the pre-summarize skip).
// It rebases the token estimate (small windows rely on it for the retry
// after overflow), books the pass as ineffective so the anti-thrash
// cooldown engages, and deliberately does NOT track savings: the adaptive
// multiplier would misread the structural no-op as a budget problem and
// widen it.
func (cm *ContextManager) bookImpossibleCompaction(ctx context.Context, compactReason string, startTime time.Time, keepCount, budgetTokens int, note string) {
	est := cm.estimatedTokens()
	cm.State.LastPromptTokens = est
	cm.State.LastPromptTokensInclusive = est
	cm.State.LastPromptIncludesCache = false
	cm.State.LastCachedPromptTokens = 0
	cm.State.LastCompactionTokens = est
	cm.State.LastCompactionMethod = "skipped"
	cm.State.IneffectiveCompactions++
	if cm.Broker != nil {
		cm.Broker.PublishMustDeliver(&CompactionDoneEvent{
			BeforeTokens:    est,
			AfterTokens:     est,
			SavingsPct:      0,
			Method:          "skipped",
			IneffectiveNote: note,
			OldMsgCount:     0,
			KeepMsgCount:    keepCount,
			Budget:          budgetTokens,
		})
	}
	if db := cm.persisterDB(); cm.SessionID != "" && db != nil {
		cooldown := int64(0)
		if cm.State.IneffectiveCompactions >= 2 {
			cooldown = time.Now().Unix() + 600
		}
		db.SetCompactionCooldown(cm.SessionID, cooldown, cm.State.IneffectiveCompactions)
	}
	observability.RecordCompaction(ctx, compactReason, time.Since(startTime), est, est, 0)
}

// reinjectActiveTask restores the live task instruction when a trim
// dropped its user prompt — the accepted path does the same in
// applyCompactedSummary. Without it the agent resumes post-trim with a
// preserved tail but no record of the deliverable.
func (cm *ContextManager) reinjectActiveTask(lastUser string) {
	if lastUser == "" {
		return
	}
	for _, m := range cm.State.Messages {
		if m.Role == "user" && m.Content == lastUser {
			return
		}
		// A previous trim already carries the task: sequential rollovers
		// must not stack one carrier per pass.
		if m.Role == "system" && m.Content == "Active task (preserve verbatim):\n"+lastUser {
			return
		}
	}
	msgs := make([]types.Message, 0, len(cm.State.Messages)+1)
	msgs = append(msgs, cm.State.Messages[0])
	msgs = append(msgs, types.SystemMsg("Active task (preserve verbatim):\n"+lastUser))
	msgs = append(msgs, cm.State.Messages[1:]...)
	cm.State.Messages = msgs
	if cm.OnMessagesReplaced != nil {
		cm.OnMessagesReplaced(msgs)
	}
}

// trimMessageBudget drops the oldest messages until the conversation
// fits the message budget — the count-triggered rollover's own target
// (yaah-l12q's residual). trimContext aims at the token window and is
// a no-op by construction when the count trigger fired (the trigger
// requires the token estimates below BOTH thresholds), so without
// this pass a count-triggered rollover that fails to summarize
// neither resolves the count nor records anything, and the count
// stays above compact_max_messages — the trigger rearms on every
// subsequent message and pays a full compaction attempt each time.
// The buffer mirrors trimContext's 80%: cut to budget*4/5 so one more
// message does not rearm. Reasoning protection wins over the budget
// the same way it wins over the token target.
func (cm *ContextManager) trimMessageBudget(budget int) {
	if budget <= 0 || len(cm.State.Messages) <= budget {
		return
	}
	target := budget * 4 / 5
	if target < 1 {
		target = 1
	}
	keepStart := len(cm.State.Messages) - target
	if keepStart < 1 {
		keepStart = 1
	}
	// Same shape as trimTokensTo: land on an exchange boundary and let
	// the windowed reasoning protection keep the newest carriers.
	keepStart = agentctx.SnapCutPastToolResults(cm.State.Messages, keepStart)
	if cm.ReasoningProtectTurns > 0 {
		keepStart = agentctx.ProtectReasoningTurns(cm.State.Messages, keepStart, cm.ReasoningProtectTurns)
	}
	cm.cutMessages(keepStart)
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
				sb.WriteString(agentctx.FormatToolStub(m) + "\n")
			} else {
				sb.WriteString(fmt.Sprintf("%s: %s\n", m.Role, m.Content))
			}
		}
		for _, tc := range m.ToolCalls {
			sb.WriteString(fmt.Sprintf("[tool_call:%s] %s\n", tc.Function.Name, excerptArgs(tc.Function.Arguments)))
		}
	}
	sb.WriteString("</conversation>\n\n")
	sb.WriteString(prompts.SummaryTemplate())

	req := types.ChatRequest{
		Model: model,
		Messages: []types.Message{
			types.SystemMsg(prompts.ChunkSummarizerRole()),
			types.UserMsg(sb.String()),
		},
	}

	resp, err := provider.Send(ctx, req)
	if err == nil && resp != nil && len(resp.Choices) > 0 && resp.Choices[0].FinishReason == "length" {
		// A chunk cut off by the output cap carries partial information;
		// returning it would install a half summary when the chunk is the
		// only one. Drop to the failure path — other chunks still feed
		// the merge, and the single-chunk case falls back to trim. The
		// drop is auditable: the chunk's history is lost from the summary.
		slog.Warn("chunk summarization truncated by output cap; dropping chunk from merge",
			"chunk", chunkIdx+1, "total", total, "model", model)
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("chunk %d/%d summarization failed: %w", chunkIdx+1, total, err)
	}
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		return "", nil
	}
	return resp.Choices[0].Message.Content, nil
}

// chunkedCompact performs multi-pass chunked summarization of old messages.
func (cm *ContextManager) chunkedCompact(ctx context.Context, oldMsgs []types.Message, compactModel string) (string, error) {
	if len(oldMsgs) == 0 {
		return "", nil
	}

	chunkBudget := int(float64(cm.ContextWindow) * agentctx.ChunkBudgetFraction)
	if chunkBudget < agentctx.MinChunkTokens {
		chunkBudget = agentctx.MinChunkTokens
	}

	chunks := agentctx.ChunkSplit(oldMsgs, chunkBudget)
	if len(chunks) <= 1 {
		return cm.summarizeChunk(ctx, oldMsgs, 0, 1)
	}

	sem := make(chan struct{}, agentctx.MaxChunkConcurrency)
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
	if depth > agentctx.MaxReduceDepth {
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
		Model: model,
		Messages: []types.Message{
			types.SystemMsg(prompts.ChunkMergerRole()),
			types.UserMsg(sb.String()),
		},
	}

	resp, err := provider.Send(ctx, req)
	if err != nil || resp == nil {
		return strings.Join(partials, "\n###\n"), nil
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" ||
		resp.Choices[0].FinishReason == "length" {
		// Empty, failed, or output-cap-truncated merges install the raw
		// partial summaries instead — each partial is complete, so the
		// join is never the half-summary shape.
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

// excerptArgsMaxLen caps tool-call arguments serialized into a compaction
// summarizer excerpt; a single oversized dispatch must not dominate the
// summarizer's input. The full arguments remain in State.Messages.
const excerptArgsMaxLen = 200

// excerptArgs bounds tool-call arguments for a compaction excerpt.
func excerptArgs(args string) string {
	return agentctx.TruncateRunes(args, excerptArgsMaxLen)
}
