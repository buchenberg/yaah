---
name: yaah-benchmark
description: Run the yaah agent loop benchmark suite and capture metrics from Jaeger traces. Use when benchmarking agent-loop performance, measuring orchestrator vs sub-agent usage, tracking token costs, or detecting regressions.
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
traces. Results are appended to `BENCHMARKS.md` in the repo root.

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

Run each scenario individually and capture metrics after each run.

### B1 — Baseline (pure reasoning, no tools)

```bash
./yaah "what is the Go standard library package for working with SQL databases? answer in one sentence."
```

Expected: 1 turn, 0 subagent, 1 llm.stream, <5s.

### B2 — Single Inline Tool

```bash
./yaah "read internal/agent/agent.go and report the number of lines it has"
```

Expected: ~2 turns, read tool executed inline, <10s.

### B3 — Sub-Agent Dispatch

```bash
./yaah "use spawn_subagent to count .go files in internal/tools/ and report total lines"
```

Expected: 1 subagent:* span, sub-agent model used, planner+sub-agent token split.

### B4 — Standard Audit (main benchmark)

This is the primary benchmark for regression detection. It exercises parallel
tool calls, multi-step reasoning, and synthesis:

```bash
./yaah 'Audit this project:
1. Count all source files grouped by directory
2. Measure total lines of source code across all directories
3. Identify the 3 largest source files by line count
4. Check the package manager file for version and dependency count
5. Synthesize all findings into a compact table under 15 lines'
```

Expected: 3-6 turns, 0-2 subagents, 5-10 tools, <60s.

### B5 — Parallel Sub-Agent Test

This test exercises parallel sub-agent dispatch with explicit delegation
instruction. It's scoped to internal/agent/ to keep token usage reasonable.

```bash
./yaah 'Analyze internal/agent/ using parallel sub-agents for each task:
1. Count total .go files and lines of code
2. Find the largest file by line count
3. List all test files (*_test.go)
Synthesize into a brief summary.'
```

Expected: 2-4 turns, 3 subagents, 10-20 tools, <30s, 100-200k tokens.

## Capturing metrics after a run

Run this after each benchmark to get the latest trace metrics:

```powershell
$body = Invoke-RestMethod -Uri 'http://localhost:16686/api/traces?service=yaah&limit=1&lookback=5m' -Method Get
foreach ($t in $body.data) {
    $spans = $t.spans
    $turns = ($spans | Where-Object { $_.operationName -eq 'agent.turn' }).Count
    $subs = ($spans | Where-Object { $_.operationName -like 'subagent:*' }).Count
    $tools = ($spans | Where-Object { $_.operationName -notin @('agent.turn','llm.stream','prompt','loop_detection','conflict.check','prune') -and $_.operationName -notlike 'subagent:*' }).Count
    $errors = ($spans | Where-Object { $_.status -eq 'error' }).Count
    $orchPrompt = 0; $orchComp = 0; $subPrompt = 0; $subComp = 0
    foreach ($s in $spans) {
        $tags = @{}; foreach ($tv in $s.tags) { $tags[$tv.key] = $tv.value }
        if ($s.operationName -eq 'agent.turn') {
            $orchPrompt += [int]$tags['turn.prompt_tokens']; $orchComp += [int]$tags['turn.completion_tokens']
        }
        if ($s.operationName -like 'subagent:*') {
            $subPrompt += [int]$tags['subagent.prompt_tokens']; $subComp += [int]$tags['subagent.completion_tokens']
        }
    }
    $orchTotal = $orchPrompt + $orchComp; $subTotal = $subPrompt + $subComp
    $total = $orchTotal + $subTotal
    $orchShare = if ($total -gt 0) { "$([math]::Round($orchTotal/$total*100))%" } else { "—" }
    $subShare = if ($total -gt 0) { "$([math]::Round($subTotal/$total*100))%" } else { "—" }
    $ts = Get-Date -Format "yyyy-MM-dd HH:mm"
    $branch = git branch --show-current
    $commit = git rev-parse --short HEAD
    Write-Host "turns=$turns  subs=$subs  tools=$tools  errors=$errors"
    Write-Host "orch=$orchTotal  sub=$subTotal  total=$total  orch%=$orchShare  sub%=$subShare"
    Write-Host "ROW: | $ts | $branch | $commit | $turns | $subs | $tools | <time> | $orchTotal | $subTotal | $total | $orchShare | $subShare | $errors |"
}
```

## Full benchmark run

Run all 5 benchmarks sequentially with metrics capture between each:

```bash
./yaah "what is the Go standard library package for working with SQL databases? answer in one sentence."
echo "--- B1 done ---"

./yaah "read internal/agent/agent.go and report the number of lines it has"
echo "--- B2 done ---"

./yaah "use spawn_subagent to count .go files in internal/tools/ and report total lines"
echo "--- B3 done ---"

./yaah 'Audit this project:
1. Count all source files grouped by directory
2. Measure total lines of source code across all directories
3. Identify the 3 largest source files by line count
4. Check the package manager file for version and dependency count
5. Synthesize all findings into a compact table under 15 lines'
echo "--- B4 done ---"

./yaah 'Analyze internal/agent/ using parallel sub-agents for each task:
1. Count total .go files and lines of code
2. Find the largest file by line count
3. List all test files (*_test.go)
Synthesize into a brief summary.'
echo "--- B5 done ---"
```

## Expected metrics range

Based on July 2026 runs with planner=`deepseek-v4-pro`,
sub-agent=`deepseek-v4-flash`:

| Benchmark | Turns | Subs | Tools | Time | Sub % |
|---|---|---|---|---|---|
| B1 (baseline) | 1 | 0 | 0 | <5s | — |
| B2 (inline) | 2-4 | 0 | 1-3 | <15s | — |
| B3 (sub-agent) | 2-4 | 1 | 1-3 | <30s | 30-60% |
| B4 (audit) | 3-6 | 0-2 | 5-10 | <60s | 0-50% |
| B5 (parallel) | 2-4 | 3 | 10-20 | <30s | 30-60% |

## Regression detection

After making changes to the agent loop, compare metrics against the baseline:

1. Run the benchmark before and after the change
2. Compare: turns should not increase significantly
3. Check error rates — should be 0 in stable runs

## Key metrics to track

- **Turns**: Orchestrator LLM calls. More turns = more planner LLM calls.
- **Sub-agents**: How many `spawn_subagent` dispatches. Zero means the model chose inline tools.
- **Sub-agent share**: Percentage of total tokens consumed by sub-agents vs orchestrator. Higher = more work offloaded to cheaper model.
- **Error rate**: Spans with `status=error`. Sporadic errors are expected (provider 429, Windows bash failures).

## Debugging benchmarks

- **No subagent spans**: Model is not calling `spawn_subagent`. Check identity prompt for delegation guidance.
- **High sub-agent error rate**: Check Jaeger for sub-agent errors — likely provider 429 (rate limit) or 503 (overloaded).
- **Sub-agent share unusually low**: Sub-agent model may be too fast/reliable, causing orchestrator to take on more work. Or identity prompt is not emphasizing delegation.
- **Excessive turns**: Loop detection may not be firing, or the model is getting stuck retrying. Check `loop_detection` spans.
