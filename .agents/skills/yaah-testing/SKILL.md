---
name: yaah-testing
description: Test yaah CLI one-shots, sub-agents, roles, OTel tracing with Jaeger, and Docker containers.
version: 1.0.0
author: local
license: MIT
platforms: [linux, macos]
prerequisites:
  env_vars: [DEEPSEEK_API_KEY]
  commands: [go, docker, curl, jq]
metadata:
  hermes:
    tags: [yaah, testing, otel, docker, debugging]
---

# yaah Testing

Smoke test and debug yaah — the CLI, sub-agents, roles, OTel traces, and
Docker containers.

## Quick smoke test

Build and run a basic one-shot to confirm the agent loop is working:

```bash
go build -trimpath -ldflags '-s -w' -o yaah .
./yaah "what is 2+2"
```

## Sub-agent testing

Test worker, reviewer, and planner roles end-to-end:

```bash
./yaah "use a worker sub-agent to list go files in internal/agent/"
./yaah "use a reviewer sub-agent to read and summarize README.md"
./yaah "use a planner sub-agent to read internal/agent/agent.go then dispatch a worker to count .go files"
```

Each test should show `>>> sub-agent: <role>` and `<<< sub-agent: <role>`
brackets in the CLI output. Verify custom roles are discovered:

```bash
./yaah "use a file-lister sub-agent to find all .md files"
```

## OTel tracing tests

Start Jaeger (v2, from the official registry) and enable tracing:

```bash
docker compose up -d jaeger
```

Run a prompt that exercises the full span hierarchy — streaming LLM, tools,
and sub-agents:

```bash
./yaah "use a worker sub-agent to count go files in internal/tools/"
```

Check that spans appear in Jaeger. All three span types should be present:

```bash
curl -s "http://localhost:16686/api/traces?service=yaah&limit=5&lookback=5m" \
  | python3 -c "
import json, sys, urllib.request
data = json.load(urllib.request.urlopen('http://localhost:16686/api/traces?service=yaah&limit=5&lookback=5m'))
ops = {}
for t in data.get('data',[]):
    for s in t['spans']:
        op = s['operationName']
        ops[op] = ops.get(op, 0) + 1
for op, n in sorted(ops.items()):
    print(f'  {op}: {n}')
"
```

Expected output should include `prompt`, `agent.turn`, `llm.stream`,
`subagent: worker`, and at least one tool span (`glob` or `ls`).

## Trace analysis

After running a few prompts, analyze the traces:

```bash
curl -s "http://localhost:16686/api/traces?service=yaah&limit=10&lookback=15m" \
  | python3 -c "
import json, sys, urllib.request
from collections import defaultdict
data = json.load(urllib.request.urlopen('http://localhost:16686/api/traces?service=yaah&limit=10&lookback=15m'))
all_spans = [s for t in data.get('data',[]) for s in t['spans']]
durs = defaultdict(list)
for s in all_spans:
    durs[s['operationName']].append(s['duration']/1000)
print('=== Latency summary ===')
for op, vals in sorted(durs.items()):
    print(f'  {op}: {len(vals)}x, avg={sum(vals)/len(vals):.0f}ms, max={max(vals):.0f}ms')

errors = [s for s in all_spans if any(f.get('key')=='error' for log in s.get('logs',[]) for f in log.get('fields',[]))]
print(f'\nErrors: {len(errors)}')
"
```

## Docker smoke test

Rebuild the image and run a one-shot inside the container. Traces should
route to Jaeger via `jaeger:4317`:

```bash
docker compose build
docker compose run --rm yaah "what is 2+2"
```

Verify `yaah doctor` inside Docker shows the correct endpoint override:

```bash
docker compose run --rm yaah doctor
```

Expected observability line: `traces → jaeger:4317`.

## Config validation

Run doctor to validate the config:

```bash
./yaah doctor
```

Checks: config file, providers, default model, OTel, home directory,
platform, editor. Red flags are `FAIL` or `WARN` statuses.

## CI-like validation

Run the same checks the GitHub Actions CI pipeline executes:

```bash
gofmt -l .                        # must be empty
go vet ./...                      # must be silent
go test ./...                     # must pass
go build -trimpath -ldflags '-s -w' -o yaah .
```

Staticcheck (requires network on first run):

```bash
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
```

## Debugging tips

- **No traces in Jaeger**: check `yaah doctor` — OTel must show `enabled`.
  Check that `YAAH_OTEL_ENABLED=true` or the config has `enabled: true`.
- **Empty API key**: run `yaah doctor` — provider checks will flag unset
  environment variables.
- **Sub-agent not spawning**: verify `agent.subagent.max_depth` is not zero.
  Check `sub_agent` middleware is not disabled in `agent.middleware.disabled`.
- **Custom role missing**: verify the `.md` file is in `.agents/roles/` or
  `~/.agents/roles/`. Run `yaah skill list` to confirm discovery paths.
- **Docker container can't reach Jaeger**: verify `jaeger:4317` is the
  endpoint. If running yaah on the host, use `localhost:4317` instead.
