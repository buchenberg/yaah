package agent

import (
	"context"
	"encoding/json"

	"github.com/buchenberg/yaah/internal/types"
)

// buildToolsForLevel selects tool definitions based on ToolsLevel config.
func (l *Loop) buildToolsForLevel() []types.ToolDef {
	switch l.Config.ToolsLevel {
	case SubAgentsOnly:
		return l.agentTools()
	default:
		return l.buildToolDefs()
	}
}

// agentTools returns only the sub-agent related tools.
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

// addUsage updates the Loop's token usage tracking from an LLM response.
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
