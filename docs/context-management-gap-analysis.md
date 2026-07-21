# Context Management: Gap Analysis & Implementation Plan

> Root cause investigation of context saturation failures (392K tokens against
> a 128K window) and a phased plan to close gaps vs. hermes-agent and opencode.
>
> **Status: IMPLEMENTED (2025-07-20).** All five phases plus anchored
> summarization, summary output budget, anti-thrashing guard, and inner loop
> summary cap are in production. See `docs/architecture.md` for the current
> design. A detailed cross-framework review of this plan is at
> `docs/context-management-plan-review.md`.

## 1. The Problem: Real-World Failure Trace

Trace `6643f9eb` ("please review the readme and make sure it's up to date"):

| Turn | Prompt Tokens | Growth | Notes |
|------|-------------|--------|-------|
| 0 | 0 | — | First turn (usage not yet reported) |
| 1 | 6,802 | — | Inner loop reads README |
| 2 | 36,202 | +29K | Inner loop greps for funcs |
| 3 | 63,188 | +27K | Reads more sections |
| 4 | 78,305 | +15K | Churns through analysis |
| 5 | 101,370 | +23K | **Passes 60% threshold (77K)** — compaction should fire |
| 6 | 124,290 | +23K | **Passes context window (128K)** |
| 7 | 148,674 | +24K | 1.2× over window |
| 8 | 173,721 | +25K | 1.4× |
| 9 | 191,192 | +17K | 1.5× |
| 10 | 202,358 | +11K | 1.6× |
| 11 | 244,420 | +42K | 1.9× |
| 12 | 263,672 | +19K | 2.1× |
| 13 | 311,977 | +48K | 2.4× |
| 14 | 349,130 | +37K | 2.7× |
| **15** | **392,708** | **+44K** | **3.1× — model returns 0 tokens, 35s dead air** |

### What's Happening

The agent is reading a file line by line via the inner loop. Each turn the inner
loop reads sections, runs greps, and returns a summary. That summary — plus the
outer model's analysis — gets appended to the conversation. After 15 turns,
unstoppable context growth hits 3× the window.

**Why compaction didn't save us:**

1. Compaction fires at 60% of 128K = **77K** (turn 5). But the char/4 heuristic
   (now replaced by `LastPromptTokens`) may have been inaccurate on earlier
   builds, so compaction may have fired late or not at all.

2. Even when compaction fires, it keeps the **last 6 messages**. If those 6
   messages (3 exchanges of inner-loop summaries + outer analysis) total 40K
   tokens, that's the floor — compaction can't go lower.

3. The LLM summarizer itself can fail (its response might be empty or a
   provider error), falling back to `trimContext` which drops oldest messages
   one at a time — a slow, token-inefficient process.

## 2. Competitive Comparison

| Strategy | yaah | hermes | opencode | crush |
|----------|------|--------|----------|-------|
| **Compaction trigger** | 60% of window | 50% | ~50% | Small-model |
| **Token estimation** | API-reported (`LastPromptTokens`) | chars/4 + API fallback | chars/4 | chars/4 |
| **Tool output pruning** | ❌ | ✅ (pre-pass before LLM) | ✅ (abbreviates) | ❌ |
| **Preservation budget** | Fixed 6 messages | Token-budget tail (~20%) | ~8K tokens | Fixed count |
| **Overflow recovery** | ✅ error-driven (50%) | ✅ + abort on 3rd failure | ✅ | ❌ |
| **Empty-response handling** | ✅ classified as overflow | ✅ | ❌ | ❌ |

### Three Gaps

1. **No tool output pruning.** Before LLM compaction, competitors cheaply replace
   large tool outputs with abbreviated markers. A `read` of a 8K-token file
   becomes `[contents of README.md — 345 lines]` instead of the full text fed
   to the LLM summarizer.

2. **Fixed-count preservation.** yaah keeps exactly 6 messages regardless of
   size. If each message is 15K tokens, that's a 90K floor — already over the
   window. Competitors keep "as many recent messages as fit in 20% of the
   context window" (~25K tokens at 128K).

3. **High proactive threshold.** 60% (77K of 128K) leaves only 21K headroom for
   the model's response + next turn's growth. Lowering to 40-50% would trigger
   compaction earlier, giving the summarizer more room to work.

## 3. Implementation Plan

### Phase 1: Tool Output Pruning (highest impact, ~60 lines)

**What:** Before LLM compaction, replace large tool result messages with
abbreviated markers. This is a cheap pre-pass that dramatically reduces the
token load sent to the LLM summarizer.

**Where:** `compactContext()` in `internal/agent/agent.go`, before the LLM call.

**How:**

```go
// pruneToolOutputs replaces large tool result messages with abbreviated
// markers to reduce token load before LLM summarization.
func (l *Loop) pruneToolOutputs(msgs []types.Message, maxLen int) []types.Message {
    out := make([]types.Message, len(msgs))
    for i, m := range msgs {
        out[i] = m
        if m.Role != "tool" || len(m.Content) <= maxLen {
            continue
        }
        lines := strings.Count(m.Content, "\n") + 1
        chars := len(m.Content)
        out[i].Content = fmt.Sprintf("[tool %s output — %d lines, %d chars]",
            m.Name, lines, chars)
    }
    return out
}
```

**Integration:** In `compactContext()`, before building the "Conversation excerpt
to summarize" string builder, call `pruneToolOutputs(oldMsgs, 200)` on the old
messages. The LLM summarizer only sees abbreviated tool outputs — it doesn't
need the full content to produce a useful summary.

**Token savings:** In the failure trace, tool results make up ~60% of message
content. Pruning at 200 chars would reduce the LLM compaction input from ~200K
tokens to ~20K tokens.

### Phase 2: Token-Budget Preservation (~40 lines)

**What:** Replace the fixed "keep 6 messages" with a token-budget approach:
keep as many recent messages as fit in 20% of the context window.

**Where:** `compactContext()` in `internal/agent/agent.go`.

**How:**

```go
// Before (fixed count):
keepRecent := 6

// After (token budget):
targetKeepTokens := l.ContextWindow / 5  // 20% of window
keepRecent := 0
keptTokens := 0
for i := len(rest) - 1; i >= 0; i-- {
    msgTokens := len(rest[i].Content) / 4
    if keptTokens + msgTokens > targetKeepTokens && keepRecent >= 2 {
        break
    }
    keptTokens += msgTokens
    keepRecent++
}
if keepRecent < 2 {
    keepRecent = 2  // always keep at least 1 exchange
}
```

**Impact:** With 128K window, the preservation budget is ~25K tokens instead of
an unbounded 6-message count. This means compaction can actually shrink context
below the window even when messages are large.

### Phase 3: Lower Proactive Threshold (~5 lines)

**What:** Drop the default compaction threshold from 60% to 50%.

**Where:** `compactContext()` in `internal/agent/agent.go`, line ~996.

**How:**

```go
// Before:
if threshold <= 0 {
    threshold = 0.6
}

// After:
if threshold <= 0 {
    threshold = 0.5
}
```

**Impact:** Compaction fires at 64K instead of 77K (128K window), leaving 64K
headroom for the model's response and next turn's growth. Combined with pruning,
this means compaction fires earlier with less data to process.

### Phase 4: Aggressive Recovery Compaction (~5 lines)

**What:** When the retry loop detects context overflow, use a 40% threshold
instead of 50%. When the outer model already failed once due to saturation, we
need to be more aggressive to ensure the retry fits.

**Where:** `getAssistantMessage()` retry logic in `internal/agent/agent.go`.

**How:**

```go
// Before (line ~808):
l.compactContext(ctx, 0.5)

// After:
l.compactContext(ctx, 0.4)
```

### Phase 5: Pre-Flight Context Guard (~15 lines)

**What:** Before sending a request to the model, check if `LastPromptTokens >
ContextWindow` and proactively compact. This catches the case where context
grew between turns without the middleware noticing (e.g., large tool results
added by the inner loop).

**Where:** `runMiddleware()` in `internal/agent/agent.go`, before the
`getAssistantMessage` call.

**How:**

```go
// Before the getAssistantMessage call:
if l.LastPromptTokens > l.ContextWindow && l.ContextWindow > 0 {
    l.compactContext(ctx, 0.5)
    messages = l.Messages
    req.Messages = messages
}
```

## 4. Expected Outcome

After all phases, re-running the README review trace would look like:

| Turn | Prompt Tokens | Notes |
|------|-------------|-------|
| 1 | 6,802 | Inner loop reads README (pruned to 200-char marker) |
| 2 | 15,000 | Small growth (outputs are pruned) |
| 3 | 25,000 | |
| 4 | 38,000 | |
| 5 | 52,000 | Compaction fires at 50% (64K) — drops to ~20K |
| 6 | 30,000 | Rebuilds |
| 7 | 45,000 | Compaction fires again |
| ... | ... | Context oscillates between 20K–60K, never exceeds window |

The combination of tool pruning + token-budget preservation + lower threshold
keeps the context safely within bounds, even on long multi-turn sessions.

## 5. Implementation Effort

| Phase | Lines | Risk | Dependency |
|-------|-------|------|-----------|
| 1 — Tool output pruning | ~60 | Low | None |
| 2 — Token-budget preservation | ~40 | Low | None |
| 3 — Lower threshold | ~5 | None | None |
| 4 — Aggressive recovery | ~5 | None | None |
| 5 — Pre-flight guard | ~15 | Low | None |
| **Total** | **~125** | | |

Phases are independent and can be implemented in any order. Phase 1 delivers the
most value alone (~80% of the improvement). Phases 2–5 are incremental
tightening.
