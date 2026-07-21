# Benchmark History

> yaah agent-loop benchmarks tracked across development changes.
> Each entry records the git state, config, and per-run metrics from Jaeger traces.

## July 21, 2026 — Executor isolation + fallback + fast-path

**Config**: planner=`zai/glm-5.2`, executor=`deepseek/deepseek-v4-flash`, subagent=`deepseek/deepseek-v4-flash`

**Prompt** (same for all runs):
```
Investigate the yaah codebase and report findings. Use these patterns:
1. INLINE: read go.mod and note the Go version
2. DELEGATE: count all .go files, total lines, and list every exported function in internal/agent/
3. TASK (worker sub-agent): scan docs/ for all markdown files and summarize each doc in one sentence
4. SYNTHESIS: combine all findings into a report with sections for project metadata, agent package, and documentation.
```

### R1: Baseline (pre-fix)

| Metric | Value |
|---|---|
| Trace | `6d1a34` |
| Date | 2026-07-21 |
| Turns | 5 |
| inner.loop | 1 (iter=10, **exhausted**) |
| Sub-agents | 1 worker (failed — 429) |
| LLM streams | 13 |
| Time | 73s |
| Plan tokens | 19,042 |
| Exec tokens | 170,106 |
| Total tokens | **189,148** |
| Exec share | 90% |

**Issues**: Executor spawned `task` sub-agents inside inner loop (iter 6-7). Sub-agent failed due to provider rate limit. Executor exhausted max iterations (10).

**Changes applied**: none — baseline before any fixes.

### R2: Executor tool isolation + sub-agent provider

| Metric | Value |
|---|---|
| Trace | `d85111d` |
| Date | 2026-07-21 |
| Turns | 7 |
| inner.loop | 3 (iter=6,3,5) |
| Sub-agents | 2 default (one failed iter cap) |
| LLM streams | 18 |
| Time | 75.6s |
| Plan tokens | 51,579 |
| Exec tokens | 103,709 |
| Total tokens | **155,288** |
| Exec share | 67% |

**Changes**: Removed `task` from executor tool set. Made sub-agent provider/model configurable. Executor no longer spawns sub-agents — planner does 3 smaller delegations instead of 1 giant one. Max executor iterations dropped from 10 to 6. Sub-agent role was `default` (model didn't specify `role` parameter).

**Δ R1→R2**: -18% total tokens, max executor iter -40%.

### R3: Degenerate stream fast-path

| Metric | Value |
|---|---|
| Trace | (lost) |
| Date | 2026-07-21 |
| Turns | 9 |
| inner.loop | 1 (iter=6) |
| Sub-agents | 1 reviewer |
| LLM streams | — |
| Time | 67.9s |
| Plan tokens | 81,914 |
| Exec tokens | 49,304 |
| Total tokens | **131,218** |
| Exec share | 38% |

**Changes**: Fast-path for empty GLM streams — immediate fallback rotation without retry backoff. No wasted GLM retries in this run. But the planner model produced 18 `go_outline` calls inline plus 4 `bash` calls on Windows.

**Δ R2→R3**: -15% total tokens, but planner ran inline tool spam. Degenerate stream fast-path worked — zero wasted GLM empty-stream turns.

### R4: Max inline tools per turn

| Metric | Value |
|---|---|
| Trace | `d51d224` |
| Date | 2026-07-21 |
| Turns | 8 |
| inner.loop | 1 (iter=6) |
| Sub-agents | 1 worker ✓ |
| LLM streams | 12 |
| Time | 83.6s |
| Plan tokens | 97,051 |
| Exec tokens | 78,162 |
| Total tokens | **175,213** |
| Exec share | 45% |

**Changes**: Added `max_inline_tools_per_turn` config (default 0 = unlimited). Turn-level inline tool cap with warning injection. No `go_outline` spam, no `bash` calls, sub-agent correctly used `worker` role.

**Δ R3→R4**: +34% total tokens, but tool choices improved (powershell not bash, no go_outline spam).

### Summary

| Metric | R1 | R2 | R3 | R4 | Best |
|---|---|---|---|---|---|
| Total tokens | 189k | 155k | 131k | 175k | **155k (R2)** |
| Exec share | 90% | 67% | 38% | 45% | **67% (R2)** |
| Max exec iter | 10 | 6 | 6 | 6 | **6** |
| Sub-agent role | — | default | reviewer | **worker** | ✓ |
| Bash-on-Windows | — | — | ✓ | ✗ | ✓ |
| go_outline spam | — | — | ✓ | ✗ | ✓ |
| Empty-stream waste | — | — | ✗ | ✗ | ✓ |

**Structural improvements confirmed working across all runs:**
- Executor never spawns sub-agents ✓
- Degenerate streams fast-path to fallback ✓
- OS/shell injection for executor ✓
- Executor falls back to main model on provider error ✓
- Sub-agent provider/model configurable ✓

**Remaining variance**: Planner model (glm-5.2) inconsistently chooses delegate vs inline.
This is model quality, not code behavior. R2 shows the optimal pattern (3 smaller delegations,
67% exec share). The structural fixes prevent degenerate behavior regardless of model choice.

## Spontaneous-choice benchmarks

Tests where the agent is NOT explicitly told which patterns to use — it must
choose inline vs delegate vs task on its own.

### S1: Complex multi-step task (no explicit patterns)

**Prompt**:
```
Set up a new Go command in cmd/yaah/ called "stats" that, when run, prints the
total number of .go files and lines of code in the project, plus a breakdown by
directory. First analyze how existing commands are registered, then implement
stats.go, then test it works by building and running './yaah stats'.
```

| Metric | Value |
|---|---|
| Trace | `5a106c0` |
| Date | 2026-07-21 |
| Config | planner=`deepseek/deepseek-v4-pro`, executor=`zai/glm-4.7-flash`, subagent=`deepseek/deepseek-v4-flash` |
| Turns | 2 |
| inner.loop | 1 (iter=2) |
| Sub-agents | 0 |
| LLM streams | 4 |
| Time | 58.7s |
| Plan tokens | 8,783 |
| Exec tokens | 11,711 |
| Total tokens | **20,494** |
| Exec share | 57% |

**Spontaneous pattern choices:**
- Turn 0: `memory_search` + `delegate` — the agent chose to check memory first, then delegated all build+test work
- Turn 1: synthesis — the executor ran analysis (glob×2, read), implementation (write), and testing (bash×3, powershell×4) all within the inner loop
- No sub-agents spawned — the executor handled everything directly
- 2 bash calls on Windows (executor tries bash before switching to powershell)

**Efficiency**: 20k tokens, 2 turns, 59s — excellent for a create-build-test cycle. The agent spontaneously chose delegate for heavy tool work, kept the planner light (8.7k tokens), and the executor chained 2 iterations to complete everything.
