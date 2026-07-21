# Context Management Plan Review: Cross-Framework Analysis

Review of `docs/context-management-gap-analysis.md` against verified source code
of hermes-agent, opencode, kilocode, and crush. Includes deep-dives into the
dual-loop root cause, competitor implementations, and concrete Go code sketches
for missing strategies.

**Status: IMPLEMENTED.** All recommended changes were applied on 2025-07-20.
The current design is documented in `docs/architecture.md`.

## Key Findings

### Corrected Competitive Comparison

| Strategy | yaah (before) | yaah (after) | hermes | opencode | kilocode | crush |
|----------|---------------|-------------|--------|----------|----------|-------|
| **Compaction trigger** | 60% of window | 50% (64K floor) | 50% (64K floor) | ~59% | ~59% + optional | ~84% |
| **Token estimation** | API primary, chars/4 fallback | API primary, chars/4 fallback | API primary, fallback | chars/4 only | chars/4 × 1.3 | API primary, fallback |
| **Pre-compaction pruning** | ❌ | ✅ tool + assistant | ✅ dedup + semantic + arg trunc | ✅ 2K trunc | ✅ 2K + clearing | N/A |
| **Preservation budget** | Fixed 6 messages | Token-budget (20% window, min 4, aligned) | Token ~10% window, min 3 | 25% usable [2K,8K] | Same | Summary-anchored |
| **Overflow recovery** | 2 attempts, 50% | 3 attempts, 40% | 3 + tier step-down | single-shot | 3 + hier chunks | proactive only |
| **Anchored summary** | ❌ | ✅ | ✅ | ✅ | ✅ | N/A |
| **Summary output budget** | ❌ uncapped | ✅ 4,096 tokens | ✅ 2K–12K | ✅ 4,096 | ✅ 4,096 | ❌ |
| **Anti-thrashing** | ❌ | ✅ <10% × 2 → skip | ✅ <10% × 2 → skip | ❌ | ❌ | ❌ |

### Changes Implemented

**High priority:**
1. Pre-compaction message pruning — replaces large tool outputs with `[tool N output — X lines]` and truncates inner-loop summaries with head+tail preservation
2. Inner loop summary cap at 2K chars — directly addresses the dual-loop root cause
3. Token-budget preservation — replaces fixed-6 with 20%-of-window budget, boundary-aligned, user-message-anchored

**Medium priority:**
4. Lower default threshold to 50% with 64K floor
5. Pre-flight context guard (last-resort safety net)
6. Aggressive recovery compaction at 40% with 3 attempts
7. Summary output budget capped at 4,096 tokens via `max_tokens`

**Lower priority:**
8. Anchored/iterative summarization — passes previous summary on re-compaction
9. Anti-thrashing guard — skips compaction if last 2 saved <10%

### Files Changed

- `internal/types/types.go` — added `MaxTokens` to `ChatRequest`
- `internal/agent/agent.go` — rewritten `compactContext()`, added `pruneMessages()`, `messageTokens()`, inner loop cap, pre-flight guard, anti-thrashing tracking
- `internal/agent/middleware_compaction.go` — updated doc comment
- `internal/agent/agent_test.go` — updated test comment
- `docs/architecture.md` — rewritten Context compaction section
- `docs/context-management-gap-analysis.md` — added implementation status
- `internal/prompts/identity.md` — noted inner loop summary cap
- `README.md` — updated stable features
