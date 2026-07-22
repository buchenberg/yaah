# yaah Benchmark History

Records are never overwritten — append a new row for every run.

## B1–B5 Scenario Suite

| Timestamp | Scenario | Turns | Subagents | Tools | LLM Streams | Time | Tokens | Notes |
|-----------|----------|-------|-----------|-------|-------------|------|--------|-------|
| 2026-07-21 18:07 | B1 Baseline | 1 | 0 | 0 | 1 | 2.5s | 7,158 | Pure reasoning, no tools |
| 2026-07-21 18:08 | B2 Inline | 4 | 0 | 3 | 4 | 12.0s | 35,249 | Direct tools only (file_info, powershell) |
| 2026-07-21 18:09 | B3 Delegate | 7 | 2 | 4 | 3 | 20.3s | 48,252 | Subagents used; first attempt hit max_iter |
| 2026-07-21 18:10 | B4 Mixed | 3 | 0 | 4 | 3 | 16.0s | 17,252 | All direct tools, zero subagents |
| 2026-07-21 18:12 | B5 Complex | 15 | 2 | 40 | 5 | 81.3s | 150,821 | Mixed: direct go.mod read + 2 parallel subagents |

**Mode:** FullTools (orchestrator sees all tools + spawn_subagent)
**Planner model:** deepseek-v4-pro
**Sub-agent model:** deepseek-v4-flash

## Standard Audit Benchmark (legacy)

| Timestamp | Branch | Commit | Turns | Subs | Tools | Time | Orch Tokens | Sub Tokens | Total Tokens | Orch % | Sub % | Errors | Notes |
|-----------|--------|--------|-------|------|-------|------|-------------|------------|--------------|--------|-------|--------|-------|
| 2026-07-21 17:22 | feature/agent-ns-reorg | 47fcb52 | 15 | 2 | 12 | 67.0s | 132,994 | — | 132,994 | 100% | — | 0 | Pre-sub-token-tracking |
| 2026-07-21 17:27 | feature/agent-ns-reorg | 47fcb52 | 21 | 4 | 25 | 92.6s | 158,180 | 142,243 | 300,423 | 53% | 47% | 0 | Sub-agent token tracking enabled |
| 2026-07-21 17:32 | feature/agent-ns-reorg | 47fcb52 | 14 | 3 | 10 | 38.6s | 93,226 | 78,653 | 171,879 | 54% | 46% | 0 | Renamed researcher→analyst, added tester role |
