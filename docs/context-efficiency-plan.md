# Context Efficiency — Remediation Plan

## Issues Identified

### 1. Pruner marks 25 items, never commits a batch
**Root cause:** Individual truncated tool results are 200-600 chars each (~50-150 tokens), below the default `prune_min_reclaim` threshold of 400 tokens. The pruner waits for enough candidates to batch-commit, but the threshold-per-item check prevents aggregation.

**Fix (2 targets):**
- **A: Add batch-aware commit.** In `Pruner.Filter()`, when `total_candidates > 0` and `sum(reclaim) >= pruneMinReclaim`, commit the batch even if individual items are below threshold.
- **B: Lower default `prune_min_reclaim`** from 400 to 200 tokens. Current tool results are small; 200 tokens reclaims ~5 stale results per cycle.

### 2. Compaction produces empty summaries ("Goal: none")
**Root cause:** `conversation_summary_preamble.md` template doesn't instruct the model to extract goals, completed work, blockers, or decisions from the conversation. The model summarizes literally what it sees — which is nothing for these fields.

**Fix:** Update `conversation_summary_preamble.md` to include explicit extraction instructions:
```
Extract from the conversation above:
- Goal: what the human asked for
- Progress: work items completed, with results
- Blocked: anything unresolved
- Decisions: architectural choices made
- State: current working tree (files modified, tests run)
```
Also update `summary_template.md` to expect and carry forward these fields.

### 3. Agent re-reads files it already saw (prompts.go read 3×)
**Root cause:** No short-term file cache. The agent reads a file, processes it, moves to the next turn, and reads the same file again because it doesn't know it was already in context.

**Fix:** Add a per-turn read cache in `agent_context.go`. Track files read in the last `N` turns (N=3). If the agent tries to read a file it already read, inject a system note: "You already read this file on turn X. Summary: <cached summary>" instead of re-reading. The cache is per-turn, not persistent — resets on compaction.

### 4. 97% of turn time is agent loop overhead (3% LLM inference)
**Root cause (multiple):**
- Pruner iterates all messages every turn even with 0 candidates
- Tool result truncation runs O(n) string operations per large result
- Prompts assembly serializes 65+ messages into JSON per turn

**Fix (incremental):**
- **A: Skip prune scan when `total_marked == 0`** — fast-path for clean sessions
- **B: Defer truncation to post-commit** — only truncate when results will be sent to the provider

## Implementation Order

1. **Pruner batch-commit + lower threshold** (biggest context win, 2 changes)
2. **Compaction template rewrite** (biggest quality win, 2 .md file edits)
3. **Read cache** (biggest redundancy fix, ~30 lines of Go)
4. **Prune scan fast-path** (cheapest performance win, 2-line guard)

## Expected Impact

| Fix | Context Saved | Latency Improvement |
|-----|---------------|---------------------|
| Pruner batch-commit | ~5-10 stale results/cycle (500-1000 tokens) | Minimal |
| Compaction template | Meaningful summaries → better multi-turn coherence | Moderate (fewer re-reads) |
| Read cache | Eliminates 30-40% of file reads | ~15% faster turns |
| Prune fast-path | No change | ~5-10ms per turn |
