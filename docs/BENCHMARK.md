# yaah Agent Loop Benchmark

> Run this after any change to the agent loop (`internal/agent/agent.go`)
> to verify performance and efficiency regressions.
>
> **Model config**: Planner=`deepseek-v4-pro`, Executor=`deepseek-v4-flash`

## Benchmarks

### 1. Baseline (pure reasoning, no tools)

```bash
./yaah "what is the Go standard library package for working with SQL databases? answer in one sentence."
```

### 2. Single Inline Tool

```bash
./yaah "read internal/agent/agent.go and report the number of lines it has"
```

### 3. Delegate Only (single delegation)

```bash
./yaah "use delegate to count .go files in internal/tools/ and report total lines via wc -l"
```

### 4. Simple (mixed inline + delegate)

```bash
./yaah "Audit internal/tools/: (1) delegate to count all .go files and total lines,
(2) read internal/tools/tools.go and list its Go standard library imports,
(3) report the combined results in a compact summary."
```

### 5. Complex (multi-delegate synthesis)

Tests todo-driven planning, multi-delegate fan-out, inline work intermixing,
and cross-directory synthesis:

```bash
./yaah "Create a todo list, then execute these steps:
1. DELEGATE: count .go files and total lines in internal/tools/
2. DELEGATE: count .go files, total lines, and list exported functions from internal/agent/
3. INLINE: read go.mod and report the Go version plus count of direct dependencies
4. SYNTHESIS: combine all findings into a table with columns: directory, files, lines, key detail.
Keep the final report under 15 lines."
```

## Metrics Capture

After each run, query the latest Jaeger trace:

```bash
python3 -c "
import json, urllib.request
from collections import Counter
data = json.load(urllib.request.urlopen(
    'http://localhost:16686/api/traces?service=yaah&limit=1&lookback=3m'))
for t in data.get('data',[]):
    turns = [s for s in t['spans'] if s['operationName']=='agent.turn']
    inner = [s for s in t['spans'] if s['operationName']=='inner.loop']
    tools = Counter()
    for s in t['spans']:
        if s['operationName'] not in ('agent.turn','inner.loop','llm.stream','prompt','loop_detection'):
            tools[s['operationName']] += 1
    plan_p = sum(int({t['key']:t['value'] for t in s['tags']}.get('turn.prompt_tokens',0)) for s in turns)
    plan_c = sum(int({t['key']:t['value'] for t in s['tags']}.get('turn.completion_tokens',0)) for s in turns)
    exec_p = sum(int({t['key']:t['value'] for t in s['tags']}.get('inner.prompt_tokens',0)) for s in inner)
    exec_c = sum(int({t['key']:t['value'] for t in s['tags']}.get('inner.completion_tokens',0)) for s in inner)
    spans_sorted = sorted(t['spans'], key=lambda s: s['startTime'])
    elapsed = (spans_sorted[-1]['startTime']+spans_sorted[-1]['duration']-spans_sorted[0]['startTime'])/1_000_000
    total_tok = plan_p+plan_c+exec_p+exec_c
    print(f'turns={len(turns)}  inner={len(inner)}  llm={len([s for s in t[\"spans\"] if s[\"operationName\"]==\"llm.stream\"])}  time={elapsed:.1f}s')
    print(f'plan={plan_p+plan_c:,}  exec={exec_p+exec_c:,}  total={total_tok:,}')
    print(f'tools={dict(tools)}')
    if exec_p > 0: print(f'exec_share={(exec_p+exec_c)/total_tok*100:.0f}%')
"
```

## Benchmark History

Models: Planner=`deepseek-v4-pro`, Executor=`deepseek-v4-flash`

| Date | # | Turns | Inner | LLM | Tools | Plan / Exec | Total Tokens | Time | Notes |
|---|---|---|---|---|---|---|---|---|---|
| 2026-07-20 | 1 | 1 | 0 | 1 | — | 27k / — | 27k | 3.2s | Pure reasoning baseline: system prompt + answer |
| 2026-07-20 | 2 | 4 | 1 | 6 | read×1, bash×1 | 116k / 22k | 139k | 16.3s | Model chose delegate over inline; 16% executor share |
| 2026-07-20 | 3 | 2 | 1 | 5 | glob×1, bash×1, read×2 | 28k / 72k | 99k | 20.1s | Clean single-delegate: executor does 72% of work |
| 2026-07-20 | 4 | 3 | 2 | 8 | glob×1, bash×4 | 83k / 92k | 175k | 22.3s | Mixed delegate+inline; 53% executor share |
| 2026-07-20 | 5 | 5 | 2 | 16 | todowrite×3, bash×9, glob×1, read×1 | 147k / 272k | 419k | 63.1s | Complex multi-delegate synthesis; 65% executor share |

### Key patterns

| Metric | Simple tasks (1-3) | Complex tasks (4-5) | Trend |
|---|---|---|---|
| Avg executor share | 29% | 59% | ↑ executor absorbs more as complexity grows |
| Avg planner tokens | 57k | 115k | ↑ planner context grows with results |
| Avg executor tokens | 47k | 182k | ↑ executor does heavy lifting |
| Tokens per LLM stream | ~15k | ~25k | ↑ context grows per stream |

