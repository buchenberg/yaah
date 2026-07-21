---
name: yaah-jaeger
description: Query and analyze yaah agent traces from Jaeger via the HTTP API. Use when examining agent-loop spans (agent.turn, inner.loop, llm.stream, subagent:*, tools), diagnosing dual-loop executor behavior, checking error rates and fallback events, or pulling trace metrics.
version: 1.0.0
author: local
license: MIT
platforms: [linux, macos, windows]
prerequisites:
  commands: [docker, curl, pwsh]
  services: [jaeger]
metadata:
  hermes:
    tags: [yaah, jaeger, tracing, observability, debugging, otel]
---

# yaah Jaeger Trace Analysis

Query and analyze yaah agent traces from Jaeger's HTTP API (`localhost:16686`).
This skill provides Python and PowerShell scripts for fetching, parsing, and
summarizing traces without opening the Jaeger UI.

## Jaeger API reference

The Jaeger all-in-one exposes these endpoints on port 16686:

| Endpoint | Purpose |
|---|---|
| `GET /api/traces?service=yaah&limit=N&lookback=Mm` | Search recent traces |
| `GET /api/traces/{traceID}` | Fetch a single trace by ID |
| `GET /api/services` | List known services |
| `GET /api/services/{service}/operations` | List operations for a service |

Query parameters for `/api/traces`:
- `service` — service name filter (required if no `operation` or `tags`)
- `operation` — operation name filter
- `limit` — max results (default 20)
- `lookback` — time window as `1h`, `30m`, `5m`
- `start` / `end` — microsecond epoch for absolute range
- `tags` — JSON tag filter e.g. `{"error":"true"}`
- `minDuration` / `maxDuration` — span duration filter (microseconds)

## yaah span types

| Span | Description |
|---|---|
| `prompt` | Root span for a single user interaction |
| `agent.turn` | One iteration of the outer agent loop |
| `llm.stream` | A streaming LLM API call |
| `inner.loop` | Dual-loop executor: planner delegated to executor |
| `subagent: <role>` | Task-tool sub-agent (worker, reviewer, planner, custom) |
| Tool names | Individual tool executions (read, write, edit, bash, etc.) |
| `conflict.check` | External file modification detection |

Key span attributes on `agent.turn`:
- `turn.delegate_calls` / `turn.inline_calls` — how many of each
- `turn.tool_call_names` — comma-separated list
- `turn.prompt_tokens` / `turn.completion_tokens`

Key span attributes on `inner.loop`:
- `inner.model` / `outer.model` — which models ran
- `inner.dedicated_provider` — was a separate executor provider used
- `inner.fallback_to_main` — did the executor fall back to the main model
- `inner.iterations` / `inner.exhausted`
- `inner.prompt_tokens` / `inner.completion_tokens`

Error spans have `status=error` and logs with `error` / `inner.error` fields.

## Quick trace summary (PowerShell)

Get the most recent traces and print per-trace metrics:

```powershell
$body = Invoke-RestMethod -Uri 'http://localhost:16686/api/traces?service=yaah&limit=5&lookback=10m' -Method Get
foreach ($t in $body.data) {
    $spans = $t.spans
    $turns = ($spans | Where-Object { $_.operationName -eq 'agent.turn' }).Count
    $inner = ($spans | Where-Object { $_.operationName -eq 'inner.loop' }).Count
    $subagent = ($spans | Where-Object { $_.operationName -like 'subagent:*' }).Count
    $streams = ($spans | Where-Object { $_.operationName -eq 'llm.stream' }).Count
    $errors = ($spans | Where-Object { $_.status -eq 'error' }).Count
    $sortSpans = $spans | Sort-Object startTime
    $elapsed = ($sortSpans[-1].startTime + $sortSpans[-1].duration - $sortSpans[0].startTime) / 1000000
    $tid = $t.traceID.Substring(0,7)
    Write-Host "trace=$tid turns=$turns inner=$inner subagent=$subagent streams=$streams errors=$errors time=$([math]::Round($elapsed,1))s"
}
```

## Detailed inner.loop analysis (PowerShell)

Inspect the executor's behavior across all inner.loop spans in a trace:

```powershell
param($traceID)
$body = Invoke-RestMethod -Uri "http://localhost:16686/api/traces/$traceID" -Method Get
$spans = $body.data[0].spans
$inner = $spans | Where-Object { $_.operationName -eq 'inner.loop' } | Sort-Object startTime
foreach ($s in $inner) {
    $tags = @{}; foreach ($t in $s.tags) { $tags[$t.key] = $t.value }
    $model = $tags['inner.model']; $etype = $tags['inner.executor_type']
    $iter = $tags['inner.iterations']; $exhausted = $tags['inner.exhausted']
    $fellBack = $tags['inner.fallback_to_main']
    $outerModel = $tags['outer.model']
    $dedicated = $tags['inner.dedicated_provider']
    $err = ''; if ($s.logs) { foreach ($l in $s.logs) { if ($l.fields) { foreach ($f in $l.fields) { if ($f.key -eq 'inner.error') { $err = $f.value } } } } }
    $dur = [math]::Round($s.duration/1000)
    Write-Host "inner.loop model=$model type=$etype dedicated=$dedicated fellBack=$fellBack iter=$iter exhausted=$exhausted outer=$outerModel dur=${dur}ms err=$err"
}
```

## Span type histogram (PowerShell)

Count all span types across recent traces:

```powershell
$body = Invoke-RestMethod -Uri 'http://localhost:16686/api/traces?service=yaah&limit=10&lookback=30m' -Method Get
$ops = @{}
foreach ($t in $body.data) {
    foreach ($s in $t.spans) {
        $op = $s.operationName
        if ($op -like 'subagent:*') { $op = 'subagent:*' }
        $ops[$op] = ($ops[$op] ?? 0) + 1
    }
}
foreach ($kv in $ops.GetEnumerator() | Sort-Object Value -Descending) {
    Write-Host "$($kv.Value)x $($kv.Key)"
}
$errors = ($body.data | ForEach-Object { $_.spans } | Where-Object { $_.status -eq 'error' }).Count
Write-Host "`nTotal errors: $errors"
```

## Error analysis (PowerShell)

Extract all errors from recent traces, grouped by span type:

```powershell
$body = Invoke-RestMethod -Uri 'http://localhost:16686/api/traces?service=yaah&limit=10&lookback=30m' -Method Get
foreach ($t in $body.data) {
    $tid = $t.traceID.Substring(0,7)
    $errSpans = $t.spans | Where-Object { $_.status -eq 'error' }
    if ($errSpans.Count -gt 0) {
        Write-Host "--- trace $tid ---"
        foreach ($s in $errSpans) {
            $err = ''; if ($s.logs) { foreach ($l in $s.logs) { if ($l.fields) { foreach ($f in $l.fields) { if ($f.key -eq 'error' -or $f.key -eq 'inner.error') { $err = $f.value } } } } }
            $tn = if ($s.tags) { ($s.tags | Where-Object { $_.key -eq 'tool.name' }).value } else { $null }
            $extra = if ($tn) { " tool=$tn" } else { "" }
            Write-Host "  $($s.operationName)${extra}: $err"
        }
    }
}
```

## Executor health check (PowerShell)

Check if the executor has been falling back. Returns a warning if fallbacks
are detected:

```powershell
$body = Invoke-RestMethod -Uri 'http://localhost:16686/api/traces?service=yaah&limit=20&lookback=1h' -Method Get
$totalInner = 0; $fellBack = 0; $errors = 0
foreach ($t in $body.data) {
    $inner = $t.spans | Where-Object { $_.operationName -eq 'inner.loop' }
    foreach ($s in $inner) {
        $totalInner++
        $tags = @{}; foreach ($tv in $s.tags) { $tags[$tv.key] = $tv.value }
        if ($tags['inner.fallback_to_main'] -eq 'true') { $fellBack++ }
        if ($s.status -eq 'error') { $errors++ }
    }
}
Write-Host "inner.loop spans: $totalInner"
Write-Host "fallbacks: $fellBack (of $totalInner)"
Write-Host "errors: $errors"
if ($fellBack -gt 0) { Write-Host "WARNING: Executor model degraded — $fellBack fallback(s) detected" }
```

## Token usage report (PowerShell)

Aggregate token usage across all turns:

```powershell
$body = Invoke-RestMethod -Uri 'http://localhost:16686/api/traces?service=yaah&limit=10&lookback=30m' -Method Get
$outerPrompt = 0; $outerComp = 0; $innerPrompt = 0; $innerComp = 0
foreach ($t in $body.data) {
    foreach ($s in $t.spans) {
        $tags = @{}; foreach ($tv in $s.tags) { $tags[$tv.key] = $tv.value }
        if ($s.operationName -eq 'agent.turn') {
            $outerPrompt += [int]$tags['turn.prompt_tokens']
            $outerComp += [int]$tags['turn.completion_tokens']
        }
        if ($s.operationName -eq 'inner.loop') {
            $innerPrompt += [int]$tags['inner.prompt_tokens']
            $innerComp += [int]$tags['inner.completion_tokens']
        }
    }
}
$total = $outerPrompt + $outerComp + $innerPrompt + $innerComp
Write-Host "Planner: prompt=$outerPrompt completion=$outerComp"
Write-Host "Executor: prompt=$innerPrompt completion=$innerComp"
Write-Host "Total: $total tokens"
if ($total -gt 0) { Write-Host "Executor share: $([math]::Round(($innerPrompt+$innerComp)/$total*100))%" }
```

## Finding a specific trace

Search by trace ID prefix:

```powershell
# Navigate to: http://localhost:16686/trace/<fullTraceID>
# Or search programmatically:
$traceID = 'c4d3d846f20778d24424f1d5a5a53cb8'
$body = Invoke-RestMethod -Uri "http://localhost:16686/api/traces/$traceID" -Method Get
$spans = $body.data[0].spans
Write-Host "Trace: $($body.data[0].traceID)"
Write-Host "Spans: $($spans.Count)"
$sortSpans = $spans | Sort-Object startTime
$elapsed = ($sortSpans[-1].startTime + $sortSpans[-1].duration - $sortSpans[0].startTime) / 1000000
Write-Host "Duration: $([math]::Round($elapsed,1))s"
```

## Checking if Jaeger is reachable

```powershell
try {
    $svc = Invoke-RestMethod -Uri 'http://localhost:16686/api/services' -Method Get -TimeoutSec 3
    Write-Host "Jaeger reachable — services: $($svc.data -join ', ')"
} catch {
    Write-Host "Jaeger not reachable — start with: docker compose up -d jaeger"
}
```

## Trace lifecycle

- **Active**: Trace is still receiving spans (agent still running)
- **Complete**: All spans received, trace is settled
- **Incomplete**: Spans remained after agent exit (crashed or disconnected)

Check for incomplete traces:

```powershell
$body = Invoke-RestMethod -Uri 'http://localhost:16686/api/traces?service=yaah&limit=20&lookback=1h' -Method Get
foreach ($t in $body.data) {
    $tid = $t.traceID.Substring(0,7)
    $turns = ($t.spans | Where-Object { $_.operationName -eq 'agent.turn' }).Count
    $inner = ($t.spans | Where-Object { $_.operationName -eq 'inner.loop' }).Count
    $sortSpans = $t.spans | Sort-Object startTime
    $elapsed = ($sortSpans[-1].startTime + $sortSpans[-1].duration - $sortSpans[0].startTime) / 1000000
    Write-Host "trace=$tid turns=$turns inner=$inner time=$([math]::Round($elapsed,1))s"
}
```

## Debugging tips

- **No traces**: Check `yaah doctor` — OTel must show `enabled`. Jaeger must be running on port 16686.
- **Spans missing attributes**: Enable `verbose: true` in config for full content/context recording.
- **Executor spans missing**: Model is calling `task` (sub-agents) instead of `delegate`. Check identity.md for the delegate guidance.
- **High inner.loop error rates**: Check `inner.fallback_to_main` — the executor model may be rate-limited or unavailable.
