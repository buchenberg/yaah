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
