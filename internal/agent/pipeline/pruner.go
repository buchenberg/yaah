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
	"strconv"
	"sync"

	"github.com/buchenberg/yaah/internal/types"
)

const (
	defaultPruneProtectTokens = 3000
	defaultPruneMinReclaim    = 1000
	defaultPruneMinTurns      = 2
)

// PruneConfig tunes soft-prune behaviour. Defaults are tuned for yaah's
// typical session size: protect 3k tokens of recent tool output and commit a
// prune once it reclaims > 1k tokens, always keeping the last 2 turns.
// Earlier thresholds (40k/20k, then 12k/4k) never fired in practice because
// realistic tool volume in short sessions (~5k) never exceeded the protect
// window; context grew unbounded as a result.
type PruneConfig struct {
	ProtectTokens  int             // tokens of recent tool output shielded from pruning
	MinReclaim     int             // minimum reclaim required to commit a prune
	MinTurns       int             // number of recent turns (by user message) always kept
	ProtectedTools map[string]bool // tool names whose results are never pruned
}

// DefaultPruneConfig returns the recommended defaults.
func DefaultPruneConfig() PruneConfig {
	return PruneConfig{
		ProtectTokens:  defaultPruneProtectTokens,
		MinReclaim:     defaultPruneMinReclaim,
		MinTurns:       defaultPruneMinTurns,
		ProtectedTools: map[string]bool{"skill": true},
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
	pruned       map[string]bool // keyed by tool-call ID
	last         PruneStats      // most recent Mark snapshot
	cumMarked    int             // survives Reset
	cumReclaimed int             // survives Reset
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
		cfg.ProtectedTools = map[string]bool{"skill": true}
	}
	return &Pruner{
		cfg:    cfg,
		pruned: make(map[string]bool),
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
	id     string
	tokens int
}

// Mark walks messages backwards from the most recent, deciding which stale
// tool results beyond the protect window should be soft-pruned. It records
// their tool-call IDs and returns stats. It does NOT mutate messages.
//
// Walk rules (see plan §3, §6.1):
//   - The last MinTurns turns (counted by user messages) are always protected.
//   - A non-index-0 system message terminates the walk (compaction summary
//     boundary — anything older is already summarized).
//   - Reaching an already-pruned tool message terminates the walk, bounding
//     per-turn cost to messages added since the last prune.
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

	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role == "user" {
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
		if m.ToolCallID == "" {
			continue
		}
		if p.pruned[m.ToolCallID] {
			break
		}
		if p.cfg.ProtectedTools[m.Name] {
			stats.ProtectedSkipped++
			continue
		}
		est := estimateToolTokens(m)
		total += est
		if total <= p.cfg.ProtectTokens {
			continue
		}
		reclaim += est
		candidates = append(candidates, pruneCandidate{id: m.ToolCallID, tokens: est})
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

	for _, c := range candidates {
		p.pruned[c.id] = true
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
	for i, m := range messages {
		out[i] = m
		if m.Role == "tool" && m.ToolCallID != "" && p.pruned[m.ToolCallID] {
			out[i].Content = pruneStub(len(m.Content))
		}
	}
	return out
}

const (
	pruneStubPrefix = "[output pruned — "
	pruneStubSuffix = " chars omitted to save context; re-run the tool if you need it again]"
)

// pruneStub is the compact replacement shown to the model in place of a
// pruned tool result. Plain ASCII so it is provider-agnostic. Built with
// strconv + concatenation (not fmt.Sprintf) to keep the per-message cost
// minimal on the hot Filter path.
func pruneStub(omittedChars int) string {
	// The Go compiler folds this 3-way concatenation into a single
	// allocation (runtime.concatstrings), keeping the hot Filter path lean.
	return pruneStubPrefix + strconv.Itoa(omittedChars) + pruneStubSuffix
}

// Reset clears the pruned set so the fresh tail is re-evaluated from
// scratch (called after compaction rebuilds messages). Cumulative
// counters are preserved for observability continuity.
func (p *Pruner) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruned = make(map[string]bool)
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
