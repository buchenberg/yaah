// Package pipeline — soft-prune core.
//
// Pruner reclaims context from stale tool-result messages without an LLM
// summarization call. It is a pure policy + state component: it decides
// which tool results are safe to elide (Mark), and produces a copy of the
// message list with those results stubbed (Filter). The originals are
// never mutated — only the ephemeral provider request sees stubs.
//
// The algorithm is a Go port of kilocode's compaction.ts prune pass,
// adapted to yaah's flat []types.Message list and ToolCallID keying.
package pipeline

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/buchenberg/yaah/internal/types"
)

const (
	defaultPruneProtectTokens = 2000
	defaultPruneMinReclaim    = 200
	defaultPruneMinTurns      = 1

	// pruneMinResultTokens is the smallest tool result worth pruning. Below
	// this a notice plus an excerpt costs nearly as much as the original, and
	// stubbing it only forces a refetch for a small file or grep hit.
	pruneMinResultTokens = 300

	// Excerpt bounds for a shrunk result: whole head/tail lines when the
	// result has enough lines, capped in bytes so one giant line cannot
	// defeat the bound.
	pruneHeadLines = 20
	pruneTailLines = 10
	pruneHeadBytes = 600
	pruneTailBytes = 300
)

// PruneConfig tunes soft-prune behaviour. Defaults are tuned for yaah's
// typical session size: protect 3k tokens of recent tool output and commit a
// prune once it reclaims > 500 tokens, always keeping the last 2 turns
// (counted as user messages OR assistant messages with tool calls, so
// single-prompt multi-iteration sessions are handled correctly).
// Earlier thresholds (40k/20k, then 12k/4k) never fired in practice because
// realistic tool volume in short sessions (~5k) never exceeded the protect
// window; context grew unbounded as a result.
type PruneConfig struct {
	ProtectTokens  int             // tokens of recent tool output shielded from pruning
	MinReclaim     int             // minimum reclaim required to commit a prune
	MinTurns       int             // number of recent turns (user msgs or assistant+tool_calls) always kept
	ProtectedTools map[string]bool // tool names whose results are never pruned
}

// DefaultPruneConfig returns the recommended defaults.
func DefaultPruneConfig() PruneConfig {
	return PruneConfig{
		ProtectTokens:  defaultPruneProtectTokens,
		MinReclaim:     defaultPruneMinReclaim,
		MinTurns:       defaultPruneMinTurns,
		ProtectedTools: map[string]bool{"skill": true, "delegate": true},
	}
}

// PruneStats reports the outcome of a Mark call and a snapshot via Stats.
// Per-call fields describe the most recent Mark; Cum* fields survive Reset.
type PruneStats struct {
	Reason           string // "post_tool" | "post_compaction" | "payload_limit"
	Candidates       int    // tool messages considered beyond the protect window
	Marked           int    // tool messages added to the pruned set this call
	ReclaimedTokens  int    // tokens estimated reclaimed this call
	ProtectedSkipped int    // candidates skipped due to ProtectedTools
	Committed        bool   // false if ReclaimedTokens <= MinReclaim
	TotalMarked      int    // current size of the pruned set (live)
	CumMarked        int    // total ever marked (survives Reset)
	CumReclaimed     int    // total ever reclaimed (survives Reset)
}

// Pruner tracks which tool-result messages have been soft-pruned.
// Safe for concurrent use: Mark, Filter, Reset, Stats, and IsPruned all
// serialize on a mutex, mirroring how Compactor state is shared.
type Pruner struct {
	mu           sync.Mutex
	cfg          PruneConfig
	pruned       map[string]bool   // keyed by tool-call ID
	previews     map[string]string // cached replacement text, keyed by tool-call ID
	last         PruneStats        // most recent Mark snapshot
	cumMarked    int               // survives Reset
	cumReclaimed int               // survives Reset
}

// NewPruner constructs a Pruner, applying defaults for any zero/negative
// config fields.
func NewPruner(cfg PruneConfig) *Pruner {
	if cfg.ProtectTokens <= 0 {
		cfg.ProtectTokens = defaultPruneProtectTokens
	}
	if cfg.MinReclaim <= 0 {
		cfg.MinReclaim = defaultPruneMinReclaim
	}
	if cfg.MinTurns <= 0 {
		cfg.MinTurns = defaultPruneMinTurns
	}
	if cfg.ProtectedTools == nil {
		cfg.ProtectedTools = map[string]bool{"skill": true, "delegate": true}
	}
	return &Pruner{
		cfg:      cfg,
		pruned:   make(map[string]bool),
		previews: make(map[string]string),
	}
}

// estimateToolTokens returns a cheap token estimate for a tool-result
// message: content length / 4 with a 10-token floor. Tool-result messages
// carry no ToolCalls, so only Content is counted.
func estimateToolTokens(m types.Message) int {
	t := len(m.Content) / 4
	if t < 10 {
		t = 10
	}
	return t
}

type pruneCandidate struct {
	id      string
	name    string
	content string
	tokens  int
}

// Mark walks messages backwards from the most recent, deciding which stale
// tool results beyond the protect window should be soft-pruned. It records
// their tool-call IDs and returns stats. It does NOT mutate messages.
//
// Walk rules (see plan §3, §6.1):
//   - The last MinTurns turns (counted by user messages OR assistant messages
//     with tool calls) are always protected.
//   - A non-index-0 system message terminates the walk (compaction summary
//     boundary — anything older is already summarized).
//   - Already-pruned tool messages are skipped. The walk does not terminate
//     here so that newer unpruned messages between the protected turn window
//     and the first pruned block are still considered. The system-message
//     boundary (compaction summary) is the walk's only hard termination
//     guarantee; without a prior compaction the walk scans to index 0.
//   - A candidate is only committed if total reclaim exceeds MinReclaim.
func (p *Pruner) Mark(messages []types.Message, reason string) PruneStats {
	p.mu.Lock()
	defer p.mu.Unlock()

	var stats PruneStats
	stats.Reason = reason
	if len(messages) == 0 {
		stats.TotalMarked = len(p.pruned)
		stats.CumMarked = p.cumMarked
		stats.CumReclaimed = p.cumReclaimed
		p.last = stats
		return stats
	}

	total := 0
	reclaim := 0
	turns := 0
	var candidates []pruneCandidate

	// Protect the newest completed tool-call batch unconditionally when it is
	// still the tail of the conversation. Without this floor the batch that
	// was just fetched can be reaped before the model has read it, which is
	// exactly the refetch loop yaah-e3qs reports. A user message after the
	// batch means the model already consumed it: the next turn has started,
	// so the batch is eligible again.
	lastBatch, lastUser := -1, -1
	for i := len(messages) - 1; i >= 0; i-- {
		if lastUser == -1 && messages[i].Role == "user" {
			lastUser = i
		}
		if lastBatch == -1 && messages[i].Role == "assistant" && len(messages[i].ToolCalls) > 0 {
			lastBatch = i
		}
		if lastUser != -1 && lastBatch != -1 {
			break
		}
	}
	protectFrom := -1
	if lastBatch > lastUser {
		protectFrom = lastBatch
	}

	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role == "user" || (m.Role == "assistant" && len(m.ToolCalls) > 0) {
			turns++
		}
		if turns < p.cfg.MinTurns {
			continue
		}
		if i > 0 && m.Role == "system" {
			break
		}
		if m.Role != "tool" {
			continue
		}
		if protectFrom >= 0 && i > protectFrom {
			continue
		}
		if m.ToolCallID == "" {
			continue
		}
		if p.pruned[m.ToolCallID] {
			continue
		}
		if p.cfg.ProtectedTools[m.Name] {
			stats.ProtectedSkipped++
			continue
		}
		est := estimateToolTokens(m)
		if est < pruneMinResultTokens {
			continue
		}
		prevTotal := total
		total += est
		if total <= p.cfg.ProtectTokens {
			continue
		}
		// Only count the portion that falls beyond the protect window.
		// The first result that straddles the boundary contributes just
		// its excess; every result after that contributes its full est.
		var reclaimable int
		if prevTotal >= p.cfg.ProtectTokens {
			reclaimable = est
		} else {
			reclaimable = total - p.cfg.ProtectTokens
		}
		reclaim += reclaimable
		candidates = append(candidates, pruneCandidate{
			id:      m.ToolCallID,
			name:    m.Name,
			content: m.Content,
			tokens:  est,
		})
	}

	stats.Candidates = len(candidates)
	stats.ReclaimedTokens = reclaim

	if reclaim <= p.cfg.MinReclaim {
		stats.Committed = false
		stats.TotalMarked = len(p.pruned)
		stats.CumMarked = p.cumMarked
		stats.CumReclaimed = p.cumReclaimed
		p.last = stats
		return stats
	}

	calls := toolCallIndex(messages)
	for _, c := range candidates {
		p.pruned[c.id] = true
		call := calls[c.id]
		name := c.name
		if name == "" {
			name = call.Function.Name
		}
		// Build the excerpt once, at commit time. Filter runs on every
		// provider request; regenerating the same preview there would turn a
		// one-off slice into a per-request scan of every pruned payload.
		p.previews[c.id] = shrinkToolResult(c.content, name, call.Function.Arguments)
	}
	stats.Marked = len(candidates)
	stats.Committed = true
	stats.TotalMarked = len(p.pruned)

	p.cumMarked += len(candidates)
	p.cumReclaimed += reclaim
	stats.CumMarked = p.cumMarked
	stats.CumReclaimed = p.cumReclaimed

	p.last = stats
	return stats
}

// Filter returns a copy of messages with pruned tool-result Content
// replaced by a compact stub. Role/Name/ToolCallID are preserved so
// tool_call_id linkage required by the Chat Completions wire format
// stays intact (the tool message is never removed). Fast path: when
// nothing is pruned, the input slice is returned unchanged (zero alloc).
//
// The original messages are not mutated: each element is copied by value
// (struct copy) and only the copy's Content string header is repointed.
func (p *Pruner) Filter(messages []types.Message) []types.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pruned) == 0 {
		return messages
	}
	out := make([]types.Message, len(messages))
	var calls map[string]types.ToolCall
	for i, m := range messages {
		out[i] = m
		if m.Role != "tool" || m.ToolCallID == "" || !p.pruned[m.ToolCallID] {
			continue
		}
		if preview, ok := p.previews[m.ToolCallID]; ok {
			out[i].Content = preview
			continue
		}
		// Defensive fallback for state created before previews were cached
		// (or a zero-value Pruner). The hot path above never indexes calls.
		if calls == nil {
			calls = toolCallIndex(messages)
		}
		call := calls[m.ToolCallID]
		name := m.Name
		if name == "" {
			name = call.Function.Name
		}
		out[i].Content = shrinkToolResult(m.Content, name, call.Function.Arguments)
	}
	return out
}

// toolCallIndex maps tool-call ID to the assistant call that issued it, so a
// pruned result can name its tool and primary argument.
func toolCallIndex(messages []types.Message) map[string]types.ToolCall {
	calls := make(map[string]types.ToolCall)
	for _, m := range messages {
		for _, tc := range m.ToolCalls {
			calls[tc.ID] = tc
		}
	}
	return calls
}

const pruneNoticePrefix = "[output pruned — "

// shrinkToolResult replaces a pruned tool result with a bounded excerpt plus
// enough identity (tool name, primary argument, original size) for the model
// to decide whether it needs the full result again. Keeping a head/tail
// excerpt avoids the old all-or-nothing stub, which discarded the very
// fragment most reads and greps are answered from.
func shrinkToolResult(content, toolName, rawArgs string) string {
	notice := pruneNotice(len(content), toolName, primaryArgument(rawArgs))
	if content == "" {
		return notice + "\n(empty result)"
	}
	var b strings.Builder
	b.Grow(len(notice) + 1 + pruneHeadBytes + pruneTailBytes + 32)
	b.WriteString(notice)
	b.WriteByte('\n')
	b.WriteString(excerpt(content))
	return b.String()
}

func pruneNotice(size int, toolName, primary string) string {
	label := strings.TrimSpace(toolName)
	if label == "" {
		label = "tool"
	}
	if primary != "" {
		label += " " + primary
	}
	return pruneNoticePrefix + strconv.Itoa(size) + " chars; " + label + "]"
}

// primaryArgument extracts the argument that best identifies a tool call for
// the stub. It prefers the common path/pattern/command keys and falls back to
// a compact rendering of the raw arguments.
func primaryArgument(rawArgs string) string {
	trimmed := strings.TrimSpace(rawArgs)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return ""
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(trimmed), &args); err == nil {
		for _, key := range []string{
			"filePath", "path", "pattern", "command", "query", "url", "name", "description",
		} {
			if v, ok := args[key].(string); ok && strings.TrimSpace(v) != "" {
				return truncateArgument(v)
			}
		}
	}
	return truncateArgument(strings.Join(strings.Fields(trimmed), " "))
}

func truncateArgument(s string) string {
	const max = 100
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "") + "…"
}

// excerpt keeps the first pruneHeadLines and last pruneTailLines when the
// content has enough lines, otherwise a bounded head and tail slice. The
// byte caps keep a single huge line from defeating the bound.
func excerpt(content string) string {
	// Count newlines before splitting: a single huge line is common for
	// minified JSON and should not allocate a one-element line slice.
	if strings.Count(content, "\n") < pruneHeadLines+pruneTailLines+1 {
		return byteExcerpt(content)
	}
	lines := strings.Split(content, "\n")
	omitted := len(lines) - pruneHeadLines - pruneTailLines
	head := truncateAtRune(strings.Join(lines[:pruneHeadLines], "\n"), pruneHeadBytes)
	tail := tailAtRune(strings.Join(lines[len(lines)-pruneTailLines:], "\n"), pruneTailBytes)
	return head + "\n[... " + strconv.Itoa(omitted) + " lines omitted ...]\n" + tail
}

func byteExcerpt(content string) string {
	headLen, tailLen := pruneHeadBytes, pruneTailBytes
	if len(content) <= pruneHeadBytes+pruneTailBytes {
		// Few lines but still worth reclaiming: keep the first half and a
		// quarter from the end rather than paying for the whole result.
		headLen, tailLen = len(content)/2, len(content)/4
	}
	head := truncateAtRune(content, headLen)
	tail := tailAtRune(content, tailLen)
	return head + "\n[... bytes omitted ...]\n" + tail
}

func truncateAtRune(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func tailAtRune(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	start := len(s) - max
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// Reset clears the pruned set so the fresh tail is re-evaluated from
// scratch (called after compaction rebuilds messages). Cumulative
// counters are preserved for observability continuity.
func (p *Pruner) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruned = make(map[string]bool)
	p.previews = make(map[string]string)
}

// Stats returns a snapshot of the most recent Mark outcome with the live
// pruned-set size and cumulative counters.
func (p *Pruner) Stats() PruneStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.last
	s.TotalMarked = len(p.pruned)
	s.CumMarked = p.cumMarked
	s.CumReclaimed = p.cumReclaimed
	return s
}

// IsPruned reports whether the given tool-call ID has been soft-pruned.
func (p *Pruner) IsPruned(id string) bool {
	if id == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pruned[id]
}
