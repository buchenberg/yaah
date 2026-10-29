# Deterministic mock provider

This directory is the deterministic counterpart to the live-model
orchestration tasks. A mock task uses the real yaah binary and the real task
fixture, but its provider is a local OpenAI-compatible server that emits a
task-specific transcript from `traps/<task>.json`.

The track exists for two jobs:

- prove that an orchestration task's mechanics do not depend on a sampled
  model response;
- give CI (or a laptop without a provider key, using `DEEPSEEK_API_KEY=mock`)
  a repeatable way to run one task twice and compare the evidence.

The server fails closed. It rejects a request whose model is not in the task's
`model_allowlist`, a request that arrives out of the trap's ordered
`steps`, or a tool the trap does not list in `allowed_tools`. It never falls
back to a real provider.

## Run

```bash
export DOCKER_HOST=unix:///run/user/$(id -u)/podman/podman.sock
export PYTHONPATH="$PWD/bench/terminal-bench"
export YAAH_BIN=/tmp/yaah-bin/yaah
export DEEPSEEK_API_KEY=mock          # the adapter requires a non-empty value

~/Code/Agentic/Frameworks/terminal-bench/.venv/bin/tb run \
  --agent-import-path yaah_agent:YaahAgent \
  -t orch-settle-discard-mock \
  --dataset-path bench/orchestration-tasks \
  --output-path /tmp/opencode/orch-mock-runs \
  --n-concurrent 1
```

The task's `docker-compose.yaml` adds the `mock` service from this directory
and waits for `/health`; `bench-orchestration.yaml` points both the parent and
sub-agent at `http://mock:8080/v1` with model `mock-orchestrator`.

After two runs, compare them without chasing timestamps or proposal ids:

```bash
python3 bench/orchestration-tasks/_mock/compare_runs.py \
  /tmp/opencode/orch-mock-runs/<run-a> \
  /tmp/opencode/orch-mock-runs/<run-b>
```

It checks the verifier verdict, message/tool sequence, token accounting, and
trace record-schema counts.

## Trap format

A trap has:

- `task`: the task id it belongs to;
- `model_allowlist`: every model name the server may receive;
- `steps`: the ordered request kinds (`orchestrator_delegate`,
  `subagent_write`, `subagent_final`, `orchestrator_discard`,
  `orchestrator_write`, `orchestrator_final` for the first task);
- `allowed_tools`: the tool calls the server may emit;
- canned response fields (`delegate`, `fizzbuzz`, `developer_report`,
  `answer_template`, `final`).

The proposal id is not baked into the trap: the server reads it from the
actual `delegate` tool result and substitutes it into the discard call and
`answer.txt`. That keeps the transcript stable while the runtime's
per-session proposal id remains real. The task verifier still checks the ID
against the settlement record.
