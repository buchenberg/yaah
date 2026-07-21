---
name: yaah-benchmark
description: Run the yaah agent loop benchmark suite and capture metrics from Jaeger traces. Use when benchmarking agent-loop performance, measuring delegate vs. subagent usage, tracking token costs between planner and executor, or detecting regressions.
version: 1.0.0
author: local
license: MIT
platforms: [linux, macos, windows]
prerequisites:
  commands: [go, docker, pwsh]
  services: [jaeger]
metadata:
  hermes:
    tags: [yaah, benchmarking, performance, otel, jaeger, profiling]
---

# yaah Benchmark Suite

Run the standard benchmark scenarios and capture per-run metrics from Jaeger
traces. This skill wraps the scenarios from `docs/BENCHMARK.md` with automated
trace capture and analysis.

See also: `yaah-testing` (CI validation), `yaah-jaeger` (trace query/parsing).

## Prerequisites

Jaeger must be running and OTel enabled in `~/.yaah/config.yaml`:

```bash
docker compose up -d jaeger
yaah doctor   # verify "traces → localhost:4317 (verbose)"
```

Build a fresh binary:

```bash
go build -trimpath -ldflags '-s -w' -o yaah .
```

## Benchmark scenarios

Run each scenario individually and capture metrics after each run. The
scenarios are designed to exercise different agent patterns:

### B1 — Baseline (pure reasoning, no tools)

```bash
./yaah "what is the Go standard library package for working with SQL databases? answer in one sentence."
```

Expected: 1 turn, 0 inner.loop, 1 llm.stream, <5s.

### B2 — Single Inline Tool

```bash
./yaah "read internal/agent/agent.go and report the number of lines it has"
```

Expected: ~2 turns, read tool executed inline, <10s.

### B3 — Explicit Delegate

```bash
./yaah "use delegate to count .go files in internal/tools/ and report total lines via wc -l"
```

Expected: 1 inner.loop span, executor model used, planner+executor token split.

### B4 — Mixed Inline + Delegate

```bash
./yaah "Audit internal/tools/: (1) delegate to count all .go files and total lines, (2) read internal/tools/tools.go and list its Go standard library imports, (3) report the combined results in a compact summary."
```

Expected: 1+ inner.loop, inline tools present, mixed dispatch.

### B5 — Complex Multi-Delegate Synthesis

Tests todo-driven planning, multi-delegate fan-out, inline intermixing, and
cross-directory synthesis:

```bash
./yaah "Create a todo list, then execute these steps:
1. DELEGATE: count .go files and total lines in internal/tools/
2. DELEGATE: count .go files, total lines, and list exported functions from internal/agent/
3. INLINE: read go.mod and report the Go version plus count of direct dependencies
4. SYNTHESIS: combine all findings into a table with columns: directory, files, lines, key detail.
Keep the final report under 15 lines."
```

Expected: 2+ inner.loop spans, mixed delegate+inline, synthesis in final turn.

## Capturing metrics after a run

Run this after each benchmark to get the latest trace metrics:

```powershell
$body = Invoke-RestMethod -Uri 'http://localhost:16686/api/traces?service=yaah&limit=1&lookback=3m' -Method Get
foreach ($t in $body.data) {
    $spans = $t.spans
    $turns = ($spans | Where-Object { $_.operationName -eq 'agent.turn' }).Count
    $inner = ($spans | Where-Object { $_.operationName -eq 'inner.loop' }).Count
    $subagent = ($spans | Where-Object { $_.operationName -like 'subagent:*' }).Count
    $streams = ($spans | Where-Object { $_.operationName -eq 'llm.stream' }).Count
    $errors = ($spans | Where-Object { $_.status -eq 'error' }).Count
    $tools = ($spans | Where-Object { $_.operationName -notin @('agent.turn','inner.loop','llm.stream','prompt','loop_detection','conflict.check') -and $_.operationName -notlike 'subagent:*' }).Count
    $planPrompt = 0; $planComp = 0; $execPrompt = 0; $execComp = 0
    foreach ($s in $spans) {
        $tags = @{}; foreach ($tv in $s.tags) { $tags[$tv.key] = $tv.value }
        if ($s.operationName -eq 'agent.turn') {
            $planPrompt += [int]$tags['turn.prompt_tokens']; $planComp += [int]$tags['turn.completion_tokens']
        }
        if ($s.operationName -eq 'inner.loop') {
            $execPrompt += [int]$tags['inner.prompt_tokens']; $execComp += [int]$tags['inner.completion_tokens']
        }
    }
    $sortSpans = $spans | Sort-Object startTime
    $elapsed = ($sortSpans[-1].startTime + $sortSpans[-1].duration - $sortSpans[0].startTime) / 1000000
    $totalTok = $planPrompt + $planComp + $execPrompt + $execComp
    Write-Host "turns=$turns  inner=$inner  subagent=$subagent  streams=$streams  tools=$tools  errors=$errors  time=$([math]::Round($elapsed,1))s"
    Write-Host "plan=$($planPrompt+$planComp)  exec=$($execPrompt+$execComp)  total=$totalTok"
    if ($execPrompt + $execComp -gt 0) {
        Write-Host "exec_share=$([math]::Round(($execPrompt+$execComp)/$totalTok*100))%"
    }
    # Detect fallbacks
    $fellBack = ($spans | Where-Object { $_.operationName -eq 'inner.loop' }).Count | ForEach-Object { 0 }
    foreach ($s in ($spans | Where-Object { $_.operationName -eq 'inner.loop' })) {
        $tags = @{}; foreach ($tv in $s.tags) { $tags[$tv.key] = $tv.value }
        if ($tags['inner.fallback_to_main'] -eq 'true') { Write-Host "WARNING: executor model fell back to main model" }
    }
}
```

## Full benchmark run

Run all 5 benchmarks sequentially with metrics capture between each:

```bash
./yaah "what is the Go standard library package for working with SQL databases? answer in one sentence."
echo "--- B1 done ---"

./yaah "read internal/agent/agent.go and report the number of lines it has"
echo "--- B2 done ---"

./yaah "use delegate to count .go files in internal/tools/ and report total lines via wc -l"
echo "--- B3 done ---"

./yaah "Audit internal/tools/: (1) delegate to count all .go files and total lines, (2) read internal/tools/tools.go and list its Go standard library imports, (3) report the combined results in a compact summary."
echo "--- B4 done ---"

./yaah "Create a todo list, then execute these steps:
1. DELEGATE: count .go files and total lines in internal/tools/
2. DELEGATE: count .go files, total lines, and list exported functions from internal/agent/
3. INLINE: read go.mod and report the Go version plus count of direct dependencies
4. SYNTHESIS: combine all findings into a table with columns: directory, files, lines, key detail.
Keep the final report under 15 lines."
echo "--- B5 done ---"
```

## Expected metrics range

Based on July 2026 runs with planner=`deepseek-v4-pro` (or `glm-5.2`),
executor=`deepseek-v4-flash` (or `glm-4.7-flash`):

| Benchmark | Turns | Inner | LLM Streams | Time | Exec Share |
|---|---|---|---|---|---|
| B1 (baseline) | 1 | 0 | 1 | <5s | — |
| B2 (inline) | 2-4 | 0-1 | 5-8 | <20s | 0-30% |
| B3 (delegate) | 2-4 | 1 | 5-8 | <25s | 50-75% |
| B4 (mixed) | 2-4 | 1 | 8-12 | <30s | 60-75% |
| B5 (complex) | 4-8 | 2-6 | 15-25 | <120s | 60-75% |

## Regression detection

After making changes to the agent loop, compare metrics against the baseline:

1. Run the benchmark before and after the change
2. Compare: turns should not increase, executor share should not decrease
3. Check `inner.fallback_to_main` — should be 0 in stable runs
4. Check error rates — errors in `inner.loop` spans indicate executor model degradation

## Key metrics to track

- **Turns**: Outer loop iterations. More turns = more planner LLM calls.
- **inner.loop count**: How many `delegate` dispatches. Zero means the model chose `task` or inline tools instead.
- **Executor share**: Percentage of total tokens consumed by executor vs planner. Higher = more work offloaded to cheaper model.
- **Fallback rate**: `inner.fallback_to_main` occurrences. Non-zero means executor model is degraded.
- **Error rate**: Spans with `status=error`. Sporadic errors are expected (provider 429, Windows bash failures).

## Debugging benchmarks

- **No inner.loop spans**: Model is not calling `delegate`. Check identity prompt for delegate guidance.
- **High inner.loop error rate**: Check Jaeger for `inner.error` — likely provider 429 (rate limit) or 503 (overloaded).
- **Executor share unusually low**: Executor model may be too fast/reliable, causing planner to take on more work. Or identity prompt is not emphasizing delegation.
- **Excessive turns**: Loop detection may not be firing, or the model is getting stuck retrying. Check `loop_detection` spans.
