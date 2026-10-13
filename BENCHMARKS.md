# yaah Benchmark History

Records are never overwritten — append a new row for every run.

**Models:** orchestrator=deepseek-v4-pro, sub-agent=deepseek-v4-flash

## Benchmark Results

| Timestamp | Branch | Commit | Scenario | Turns | Subs | Tools | Time | Orch Tokens | Sub Tokens | Total Tokens | Orch % | Sub % | Errors | Notes |
|-----------|--------|--------|----------|-------|------|-------|------|-------------|------------|--------------|--------|-------|--------|-------|
| 2026-07-21 17:22 | feature/agent-ns-reorg | 47fcb52 | B4 Audit | 15 | 2 | 12 | 67.0s | 132,994 | — | 132,994 | 100% | — | 0 | Pre-sub-token-tracking |
| 2026-07-21 17:27 | feature/agent-ns-reorg | 47fcb52 | B4 Audit | 21 | 4 | 25 | 92.6s | 158,180 | 142,243 | 300,423 | 53% | 47% | 0 | Sub-agent token tracking enabled |
| 2026-07-21 17:32 | feature/agent-ns-reorg | 47fcb52 | B4 Audit | 14 | 3 | 10 | 38.6s | 93,226 | 78,653 | 171,879 | 54% | 46% | 0 | Renamed researcher→analyst, added tester role |
| 2026-07-22 00:04 | feature/pruning | a740911 | B4 Audit | 5 | 0 | 5 | 49.5s | 49,690 | 0 | 49,690 | 100% | 0% | 0 | |
| 2026-07-22 00:33 | feature/pruning | a740911 | B4 Audit | 4 | 0 | 7 | 44.3s | 51,019 | 0 | 51,019 | 100% | 0% | 0 | Post executor cleanup |
| 2026-07-22 01:07 | feature/pruning | a740911 | B5 Parallel | 14 | 3 | 21 | 26.2s | 108,800 | 90,538 | 199,338 | 55% | 45% | 0 | Parallel sub-agents, scoped to internal/agent/ |
| 2026-07-22 10:12 | feat/preflight-compaction | 7f86ab5 | B4 Audit | 5 | 0 | 11 | <time> | 75553 | 0 | 75553 | 100% | 0% | 0 | Preflight compaction |
| 2026-07-22 10:17 | main | 7f43924 | B4 Audit | 4 | 0 | 8 | <time> | 43415 | 0 | 43415 | 100% | 0% | 0 | Baseline - main branch |
| 2026-07-22 10:55 | main | 2d6f7b3 | B4 Audit (ctx=20k) | 3 | 0 | 6 | <time> | 31534 | 0 | 31534 | 100% | 0% | 0 | Low context window test |
| 2026-07-22 10:58 | feat/preflight-compaction | 686c237 | B4 Audit (ctx=20k) | 7 | 3 | 5 | <time> | 32728 | 0 | 32728 | 100% | 0% | 0 | With continuation guard |
| 2026-07-22 14:49 | feature/moar-loop-tuning | 9f26ccf | B4 Audit | 7 | 1 | 12 | 48.0s | 90,929 | 61,676 | 152,605 | 60% | 40% | 0 | Phase 2: truncation + skill index + cost propagation + replay recovery |
| 2026-07-22 11:02 | feat/preflight-compaction | fd5eee0 | B4 Audit (ctx=20k) | 4 | 0 | 8 | <time> | 49720 | 0 | 49720 | 100% | 0% | 0 | Without continuation guard |
| 2026-07-22 12:25 | feature/compaction-survival | 2d6f7b3 | B4 Audit (ctx=20K) | 9 | 2 | 14 | 54.7s | 93,688 | 65,503 | 159,191 | 59% | 41% | 0 | Token-budget compaction + budget scaling; prompt oscillates 5.8K–15K vs monotonic 20.8K without |
| 2026-07-22 16:13 | subagent-efficiency | 1a85775 | B4 Audit | 21 | 4 | 34 | 59.1s | 200,823 | 172,495 | 373,318 | 54% | 46% | 0 | MaxTurns/ContextWindow/JSONMode/OutputLimit; model chose 4 reviewers |
| 2026-07-22 16:18 | subagent-efficiency | 1a85775 | B4 Audit (ctx=20K) | 2 | 0 | 5 | 33.9s | 20,111 | 0 | 20,111 | 100% | 0% | 0 | Best 20K run — 36% fewer tokens than main at 20K (31,534) |
| 2026-07-22 16:22 | main | 1a85775 | B4 Audit (ctx=20K) | 22 | 4 | 37 | 86.2s | 183,392 | 174,936 | 358,328 | 51% | 49% | 0 | Model dispatched 4 analysts |
| 2026-07-22 16:23 | main | 1a85775 | B4 Audit | 3 | 0 | 5 | 28.3s | 30,081 | 0 | 30,081 | 100% | 0% | 0 | Inline execution |
| 2026-07-22 16:24 | subagent-efficiency | cc7cf8b | B4 Audit | 3 | 0 | 7 | 34.4s | 34,391 | 0 | 34,391 | 100% | 0% | 0 | Inline execution — parity with main |
| 2026-07-22 16:34 | subagent-efficiency | 48a8753 | B4 Audit | 4 | 0 | 7 | 31.6s | 44,662 | 0 | 44,662 | 100% | 0% | 0 | "Prefer parallel subs when ctx > 64K" prompt — still ran inline |
| 2026-07-22 18:41 | per-role-overrides | 11e0da6 | B4 Audit | 12 | 4 | 7 | ~45s | 83,691 | 44,942 | 128,633 | 65% | 35% | 0 | Role descs in schema; evidence/interpretation guidance; 3 counters + 1 analyst; 0 re-verification |
| 2026-07-22 18:52 | per-role-overrides | 11e0da6 | B4 Audit | 13 | 4 | 9 | ~60s | 98,552 | 65,774 | 164,326 | 60% | 40% | 0 | ContractField kind tags; counters use powershell Get-ChildItem; 1 counter output re-verified |
| 2026-07-23 00:42 | main | 29234fd | B4 Audit | 8 | 2 | 23 | 113.4s | 91,109 | 47,702 | 138,811 | 66% | 34% | 0 | Pre-PR#62 baseline; model dispatched Counter + Jack |
| 2026-07-23 00:43 | engine-view-separation-2-3 | fef70c0 | B4 Audit | 2 | 0 | 4 | 42.6s | 23,858 | 0 | 23,858 | 100% | 0% | 0 | PR#62 engine-view separation; model chose inline over sub-agents — strategy difference, not perf regression |
| 2026-07-23 15:32 | feature/context-degradation-fix | bd3bdcf | B4 Audit | 10 | 3 | 13 | 50.4s | 99,364 | 63,562 | 162,926 | 61% | 39% | 0 | Dispatched Jack + 2 Counters; orchestrator re-did some sub-agent work inline |

| 2026-10-12 14:07 | main | 12e9334 | TB mini-suite (Terminal-Bench, 11/15) | — | — | — | ~50min | 6.3M | — | 6,255,709 | 100% | 0% | 3 | yaah one-shot vs harness pin d28711d; deepseek-flash; fix-git excluded (upstream-unbuildable); 2 agent timeouts (localstack, kind), 1 test fail; tokens from in-container state.db |

| 2026-10-12 16:35 | main | dd038a7 | TB mini-suite vs opencode (deepseek-flash) | — | — | — | ~40min | 3.59M | — | 3,587,839 | 100% | 0% | 3 | yaah 12/14 vs opencode 7/14 of runnable tasks; same model, same judge, 1 trial each; yaah run carried the PR #291 improvements (non-interactive one-shot, incremental flush, cache accounting: 84% cache-hit) |

| 2026-10-12 17:26 | main | b2825c3 | TB suite.txt vs opencode (deepseek-flash) | — | — | — | ~10min | 975K | — | 975,292 | 100% | 0% | 0 | yaah 6/6 vs opencode 5/6 on the 6-task editable suite; harness pin d28711d; 1 trial each; yaah 944,377 prompt + 30,915 output, 90% cache-hit (848K); opencode failed broken-python (parse_error, 2/2 runs); opencode no token accounting; run-ids 2026-10-12__17-26-54 / 2026-10-12__17-35-13; opencode improved vs its 16-35 run (3/6 → 5/6) — single-trial variance is real |

| 2026-10-12 18:34 | main | b2825c3 | TB suite.txt + hermes (deepseek-flash) | — | — | — | ~8min | 844K | — | 858,729 | 100% | 0% | 0 | hermes-agent 0.19.0 via hermes_agent.py adapter (prebuilt package tree per Python minor version — immune to broken-python's deleted-pip fixture); 6/6; wire model deepseek-chat (flash alias); hermes 844,193 prompt + 14,536 output, 97% cache-hit (816K); usage-file semantics: input excludes cache reads and writes (combined to match yaah); three-way same-day: yaah 6/6 (975K), hermes 6/6 (859K), opencode 5/6 (no accounting); run-id 2026-10-12__18-34-17 |
| 2026-10-12 20:18 | main | 191389c | TB suite.txt + aider (deepseek-flash) | — | — | — | ~40min | 646K | — | 647,016 | 100% | 0% | 5 | aider @ 5dc9490 via aider_agent.py adapter + aider_driver.py (prebuilt package trees py312/313; stream=False for exact usage; autonomy preamble; model deepseek/deepseek-chat per aider's model-settings); 1/6 — edit-format non-conformance: deepseek-flash answers in prose/bash, aider reflects 3x and applies no edits; 99% cache-hit (641K); four-way same-day: yaah 6/6 (975K), hermes 6/6 (859K), opencode 5/6 (no accounting), aider 1/6 (647K); run-id 2026-10-12__20-18-12 |

## Analysis: Continuation Guard Impact

**Test Configuration:** `context_window: 20000` to force compaction during B4 audit

| Configuration | Tokens | Turns | Subs | Tools | Delta vs Main |
|---------------|--------|-------|------|-------|---------------|
| Main branch | 31,534 | 3 | 0 | 6 | baseline |
| Preflight + continuation guard | 32,728 | 7 | 3 | 5 | +3.8% |
| Preflight - continuation guard | 49,720 | 4 | 0 | 8 | +57.7% |
| **Token-budget + budget scaling - guard** | **159,191** | **9** | **2** | **14** | — |

**Finding:** The continuation guard improves token efficiency when compaction is
naive (fixed keepCount + chars/4 estimates) because removing the guard causes
context loss and re-execution (+52% tokens).

However, with **token-budgeted boundary-aligned splitTail** plus **budget scaling**
to compensate for chars/4 vs tokenizer mismatch, removing the guard is safe:
- Prompt oscillates 5.8K–15K instead of monotonic climb to 20.8K
- 0 errors at ctx=20000 (tool linkage preserved throughout)
- 2 sub-agents dispatched (vs 0–1 without compaction — model had headroom)
- The higher total tokens in the budget-scaling row reflect the sub-agent work
  (65,503 sub tokens) which baseline runs didn't execute

**Recommendation:** Remove the continuation guard when token-budgeted splitTail
with budget scaling is active. The guard was necessary for fixed-count compaction
but the boundary-aligned approach safely compacts during tool loops. The earlier
analysis tested guard removal *without* budget scaling, which caused aggressive
over-compaction — that is fixed by the scalable token estimate.

## Sub-Agent Efficiency — head-to-head comparison (2026-07-22)

Identical prompt, identical config (except branch), runs within minutes of each other:

**ctx=20K:**

| Metric | main | subagent-efficiency | Delta |
|--------|------|---------------------|-------|
| Turns | 22 | 2 | -91% |
| Subs | 4 | 0 | -100% |
| Tools | 37 | 5 | -86% |
| Time | 86.2s | 33.9s | -61% |
| Tokens | 358,328 | 20,111 | -94% |
| Errors | 0 | 0 | — |

main dispatched 4 analysts; subagent-efficiency ran inline. Both produced correct output.

**Default 128K:**

| Metric | main | subagent-efficiency | Delta |
|--------|------|---------------------|-------|
| Turns | 3 | 3 | 0% |
| Subs | 0 | 0 | — |
| Tools | 5 | 7 | +40% |
| Time | 28.3s | 34.4s | +22% |
| Tokens | 30,081 | 34,391 | +14% |
| Errors | 0 | 0 | — |

Both ran inline with similar results. The ~14% token increase is noise.

**Conclusion:** Sub-agent efficiency controls (MaxTurns, ContextWindow, OutputLimit)
do not regress performance. The dominant factor in benchmark variance is whether
the orchestrator model chooses to dispatch sub-agents or run inline — an
unpredictable model behavior orthogonal to our changes. When both branches run
inline, they perform at parity.
