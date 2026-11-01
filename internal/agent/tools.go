package agent

import (
	"context"
	"encoding/json"

	"github.com/buchenberg/yaah/internal/agent/llm"
	"github.com/buchenberg/yaah/internal/types"
)

// buildToolsForLevel selects tool definitions based on ToolsLevel config.
func (l *Loop) buildToolsForLevel() []types.ToolDef {
	switch l.Config.ToolsLevel {
	case SubAgentsOnly:
		return l.agentTools()
	case ReadOnlyTools:
		return l.readOnlyTools()
	default:
		return l.buildToolDefs()
	}
}

// readOnlyToolNames is the plan-mode surface: inspection only. Shell, edit,
// write, patch, delete, replace, plan (which writes .agents/plans), and
// delegation are absent so a plan-mode turn cannot mutate the workspace or
// widen its own authority.
var readOnlyToolNames = map[string]bool{
	"read":       true,
	"grep":       true,
	"glob":       true,
	"ls":         true,
	"file_info":  true,
	"go_outline": true,
	"calculate":  true,
	"diff":       true,
	"question":   true,
}

// allowsTool enforces the advertised tool level at execution time. Hiding a
// tool definition is not enough: dialect recovery can synthesize calls the
// model never saw in the tool array.
func (l *Loop) allowsTool(name string) bool {
	switch l.Config.ToolsLevel {
	case ReadOnlyTools:
		return readOnlyToolNames[name]
	case SubAgentsOnly:
		return subAgentToolNames[name]
	default:
		return true
	}
}

func (l *Loop) readOnlyTools() []types.ToolDef {
	if l.Registry == nil {
		return nil
	}
	var defs []types.ToolDef
	for _, name := range l.Registry.List() {
		if !readOnlyToolNames[name] {
			continue
		}
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
	return defs
}

var subAgentToolNames = map[string]bool{"delegate": true, "list_subagents": true}

// agentTools returns only the sub-agent related tools.
func (l *Loop) agentTools() []types.ToolDef {
	var defs []types.ToolDef
	for _, name := range l.Registry.List() {
		if subAgentToolNames[name] {
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

// addUsage updates the Loop's token usage tracking from an LLM response.
// It is the single point where per-call usage lands: the running totals
// (including the cache-hit fold) and the OnUsage hook that lets the
// session persist usage incrementally — a session killed mid-run
// (timeout, crash) still reports what it spent.
func (l *Loop) addUsage(u types.Usage) {
	running := l.addUsageLocked(u)
	if l.OnUsage != nil {
		// Outside the usage lock: the hook writes to the session DB
		// and must not hold up LLM accounting. Out-of-order snapshots
		// across concurrent tool goroutines are handled downstream by
		// the session's monotonic flush.
		l.OnUsage(running)
	}
}

// addUsageLocked mutates the loop's running totals and returns the
// snapshot after this call.
func (l *Loop) addUsageLocked(u types.Usage) types.Usage {
	l.usageMu.Lock()
	defer l.usageMu.Unlock()
	l.State.TotalTokens.PromptTokens += u.PromptTokens
	l.State.TotalTokens.CompletionTokens += u.CompletionTokens
	l.State.TotalTokens.TotalTokens += u.TotalTokens
	if u.PromptIncludesCache {
		l.State.TotalTokens.PromptIncludesCache = true
	}
	l.State.LastPromptTokens = u.PromptTokens
	l.State.LastPromptTokensInclusive = u.InclusivePromptTokens()
	l.State.LastPromptIncludesCache = u.PromptIncludesCache
	if d := u.CompletionTokensDetails; d != nil {
		l.State.TotalReasoningTokens += d.ReasoningTokens
	}
	if cached := u.CacheTokens(); cached > 0 {
		l.State.TotalCachedPromptTokens += cached
		l.State.TotalTokens.CachedPromptTokens += cached
		l.State.LastCachedPromptTokens = cached
	} else {
		l.State.LastCachedPromptTokens = 0
	}
	return l.State.TotalTokens
}

// addSubAgentUsage folds a child dispatch's usage into the loop totals without
// touching LastPromptTokens/LastCachedPromptTokens: those describe the
// parent's current context occupancy, and replacing them with a child's
// cumulative spend would make compaction act on the wrong conversation.
func (l *Loop) addSubAgentUsage(u types.Usage) {
	l.usageMu.Lock()
	l.State.TotalTokens.PromptTokens += u.PromptTokens
	l.State.TotalTokens.CompletionTokens += u.CompletionTokens
	l.State.TotalTokens.TotalTokens += u.TotalTokens
	if u.PromptIncludesCache {
		l.State.TotalTokens.PromptIncludesCache = true
	}
	l.State.SubAgentTokens.PromptTokens += u.PromptTokens
	l.State.SubAgentTokens.CompletionTokens += u.CompletionTokens
	l.State.SubAgentTokens.TotalTokens += u.TotalTokens
	if d := u.CompletionTokensDetails; d != nil {
		l.State.TotalReasoningTokens += d.ReasoningTokens
	}
	if cached := u.CacheTokens(); cached > 0 {
		l.State.TotalCachedPromptTokens += cached
		l.State.TotalTokens.CachedPromptTokens += cached
		l.State.SubAgentTokens.CachedPromptTokens += cached
	}
	running := l.State.TotalTokens
	l.usageMu.Unlock()
	if l.OnUsage != nil {
		l.OnUsage(running)
	}
}

// addRecoveryUsage folds usage billed by an attempt that was replayed after
// output-budget exhaustion. It updates running totals but deliberately leaves
// LastPromptTokens/LastCachedPromptTokens alone: those describe the final,
// successful attempt's context occupancy, not the discarded attempt's prompt.
func (l *Loop) addRecoveryUsage(u types.Usage) {
	if u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0 {
		return
	}
	l.usageMu.Lock()
	l.State.TotalTokens.PromptTokens += u.PromptTokens
	l.State.TotalTokens.CompletionTokens += u.CompletionTokens
	l.State.TotalTokens.TotalTokens += u.TotalTokens
	if u.PromptIncludesCache {
		l.State.TotalTokens.PromptIncludesCache = true
	}
	if d := u.CompletionTokensDetails; d != nil {
		l.State.TotalReasoningTokens += d.ReasoningTokens
	}
	if cached := u.CacheTokens(); cached > 0 {
		l.State.TotalCachedPromptTokens += cached
		l.State.TotalTokens.CachedPromptTokens += cached
	}
	running := l.State.TotalTokens
	l.usageMu.Unlock()
	if l.OnUsage != nil {
		l.OnUsage(running)
	}
}

// foldFailedCallUsage accounts for spend from a failed LLM call without
// touching context occupancy. RecoveryUsage holds the first, budget-exhausted
// attempt; Usage holds the final failing attempt. Both are billable.
func (l *Loop) foldFailedCallUsage(result llm.CallResult) {
	l.addRecoveryUsage(result.RecoveryUsage)
	l.addRecoveryUsage(result.Usage)
}

// OwnUsage returns the loop's own model usage: totals minus the child usage
// folded into them. Recording this per dispatch keeps nested delegate spend
// from being counted once in the parent's fact and again in the child's.
func (l *Loop) OwnUsage() types.Usage {
	l.usageMu.Lock()
	defer l.usageMu.Unlock()
	own := types.Usage{
		PromptIncludesCache: l.State.TotalTokens.PromptIncludesCache,
		PromptTokens:        l.State.TotalTokens.PromptTokens - l.State.SubAgentTokens.PromptTokens,
		CompletionTokens:    l.State.TotalTokens.CompletionTokens - l.State.SubAgentTokens.CompletionTokens,
		TotalTokens:         l.State.TotalTokens.TotalTokens - l.State.SubAgentTokens.TotalTokens,
		CachedPromptTokens:  l.State.TotalTokens.CachedPromptTokens - l.State.SubAgentTokens.CachedPromptTokens,
	}
	if own.PromptTokens < 0 {
		own.PromptTokens = 0
	}
	if own.CompletionTokens < 0 {
		own.CompletionTokens = 0
	}
	if own.TotalTokens < 0 {
		own.TotalTokens = 0
	}
	if own.CachedPromptTokens < 0 {
		own.CachedPromptTokens = 0
	}
	return own
}

// compactMessagesIsolated runs context compaction against an isolated
// LoopState view so overflow recovery triggered from inside LLM.Call —
// and the pipeline's compaction drains — never mutate the loop's live
// message state (single-writer invariant, review finding B6). The
// runMiddleware loop owns l.State.Messages and adopts the returned
// slice at its defined compaction point. Compaction bookkeeping
// (budget adaptation, ineffective-compaction counters, persistence
// rebasing via OnMessagesReplaced) is preserved across the isolation.
func (l *Loop) compactMessagesIsolated(ctx context.Context, messages []types.Message, threshold float64) []types.Message {
	saved := l.State
	isolated := saved
	isolated.Messages = messages
	l.State = isolated
	defer func() {
		adopted := l.State
		adopted.Messages = saved.Messages // caller owns the slice; it adopts the return value
		l.State = adopted
	}()
	l.compactContext(ctx, threshold)
	return l.State.Messages
}

// llmCompact satisfies the llm.Compactor interface. It runs compaction
// against an isolated state view and returns the compacted slice; it
// never writes l.State.Messages directly (B6).
func (l *Loop) llmCompact(ctx context.Context, messages []types.Message, threshold float64) []types.Message {
	return l.compactMessagesIsolated(ctx, messages, threshold)
}

// llmTrim reduces context deterministically by removing the oldest
// messages. It is used as a fallback when the LLM returns an empty
// stream, indicating the context is too large even for summarization.
// Like llmCompact it runs against an isolated state view (B6).
func (l *Loop) llmTrim(ctx context.Context, messages []types.Message) []types.Message {
	saved := l.State
	isolated := saved
	isolated.Messages = messages
	l.State = isolated
	defer func() {
		adopted := l.State
		adopted.Messages = saved.Messages
		l.State = adopted
	}()
	l.trimContext()
	return l.State.Messages
}
