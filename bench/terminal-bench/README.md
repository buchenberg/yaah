# Terminal-Bench suite (yaah)

A small Terminal-Bench task subset for CLI-vs-CLI comparison at
flash-model prices, plus the yaah agent adapter that plugs yaah into
the Terminal-Bench harness as an installed agent.

- Tasks: `suite.txt` (6 tasks, editable — unfrozen; cite the file's
  commit alongside any score. The frozen 15- and 12-task lists live
  in git history; recorded runs cite their pinned commits)
- Agent adapter: `yaah_agent.py` (import path `yaah_agent:YaahAgent`)
- Comparison adapters: `hermes_agent.py` for hermes (import path
  `hermes_agent:HermesAgent`; ships a prebuilt package tree per Python
  minor version — see its module docstring for the tarball build)
- Container setup: `yaah-setup.sh.j2` (config written in-container)
- Runner: `run.sh`
- Results: [RESULTS.md](RESULTS.md)

## One-time host setup

Podman stands in for Docker (the harness shells out to `docker compose`
and talks to the Docker API socket):

```bash
# 1. The podman socket (rootless)
systemctl --user enable --now podman.socket
export DOCKER_HOST=unix:///run/user/$(id -u)/podman/podman.sock

# 2. compose support + the docker->podman shim
uv tool install podman-compose
cat > ~/.local/bin/docker <<'EOF'
#!/bin/sh
if [ "$1" = "compose" ]; then shift; exec podman-compose "$@"; fi
exec podman "$@"
EOF
chmod +x ~/.local/bin/docker

# 3. A Terminal-Bench checkout with its venv (defaults below)
git clone https://github.com/laude-institute/terminal-bench \
  ~/Code/Agentic/Frameworks/terminal-bench
cd ~/Code/Agentic/Frameworks/terminal-bench
git checkout d28711d   # PINNED: the adapter imports internal harness
                      # modules and tasks move — reruns are comparable
                      # only against the pinned commit
uv venv .venv && uv pip install -e .
```

## Running

```bash
export DEEPSEEK_API_KEY=...      # required; the same key your yaah config interpolates
./run.sh                          # the 6-task suite (default, suite.txt)
./run.sh hello-world broken-python  # the 2-task smoke
SUITE=my-tasks.txt ./run.sh       # any task-list file in this directory
YAAH_MODEL=<model> ./run.sh       # any tier you like; default deepseek-flash
```

The runner builds the static yaah binary if `$YAAH_BIN` is absent, reads
the API key from the environment (never from the yaah config — its
`api_key` is typically the `${DEEPSEEK_API_KEY}` placeholder), and runs
the harness with the agent adapter on `PYTHONPATH`.

Benching the comparison CLIs (same suite and key; see RESULTS.md for
recorded runs):

```bash
# opencode (harness installed-agent; no token accounting)
.venv/bin/tb run --agent opencode --model deepseek/deepseek-flash \
  -t hello-world -t broken-python -t csv-to-parquet \
  -t jsonl-aggregator -t assign-seats -t gomoku-planner \
  --dataset-path original-tasks --output-path runs --n-concurrent 1

# hermes (adapter on PYTHONPATH; full usage accounting via --usage-file)
PYTHONPATH=<this-dir> .venv/bin/tb run \
  --agent-import-path hermes_agent:HermesAgent \
  -t hello-world -t broken-python -t csv-to-parquet \
  -t jsonl-aggregator -t assign-seats -t gomoku-planner \
  --dataset-path original-tasks --output-path runs --n-concurrent 1
```

Model config inside the task container: `agents.default.model` and
`small_model` are the model (default deepseek-flash); sub-agents inherit
it (`small_model` is compaction-only). `approval: allow` — benchmark
sessions are non-interactive; the task container is the sandbox, same as
every other CLI agent benched here.

## Orchestration suite and combined runs

The orchestration task set lives in `../orchestration-tasks` and uses its own
dataset root. Run it directly:

```bash
DATASET=bench/orchestration-tasks \
SUITE=bench/orchestration-tasks/suite.txt \
bench/terminal-bench/run.sh
```

Or run both suites with separate scoreboards:

```bash
./bench/run-all.sh                 # coding + orchestration
SUITES=orch ./bench/run-all.sh     # orchestration only
```

`run-all.sh` writes `<OUTPUT_PATH>/<run-id>/metrics.json` per suite and a
combined `summary-<timestamp>.txt`. It does not merge the scores: coding
measures outcome accuracy, orchestration measures trace compliance.

Oracle validation for an orchestration task: `tb run --agent oracle` stages
`solution.sh` at `/oracle/solution.sh`, and each task's `run-tests.sh` sees
that file and sets `ORCH_ORACLE=1`. For a manual run, set it yourself:
`ORCH_ORACLE=1 bash run-tests.sh`. The env flag is the only skip signal for
missing trace/state evidence; the agent cannot set the verifier's environment,
so broken capture fails instead of silently skipping the orchestration layers.

## Reading results

- Verdicts: `runs/<run-id>/results.json` (`is_resolved` per task).
- Tokens: `runs/<run-id>/<task>/*/agent-logs/yaah-home/state.db` —
  `sessions.tokens_in/tokens_out`. The container's whole `YAAH_HOME` is
  placed under the mounted agent-logs path so `state.db` survives the
  container. Sub-agent traces (when a task dispatches sub-agents) land
  in `agent-logs/yaah-traces/` the same way.

The persisted `yaah-home/config.yaml` keeps the `${DEEPSEEK_API_KEY}`
placeholder — no literal secret is written anywhere under the mounted
agent-logs; yaah resolves the key from the session environment.

Per-task metrics (verdict, agent wall time, token accounting, and — when a
model price is known — cost) for one or more runs:

```bash
python3 extract_metrics.py ~/Code/Agentic/Frameworks/terminal-bench/runs <run-id> [run-id ...]
```

`--json` emits the common `terminal-bench.metrics.v1` record used by all arms:

```json
{
  "schema": "terminal-bench.metrics.v1",
  "runs": [{
    "arm": "yaah",
    "model": "deepseek-flash",
    "effort": null,
    "tasks": [{
      "task_id": "hello-world",
      "resolved": true,
      "tokens": {
        "input": 0, "output": 0, "reasoning": null,
        "cache_read": 0, "cache_write": 0
      },
      "cost_usd": 0.0,
      "steps": 0,
      "wall_time_s": 0,
      "subagent_tokens": 0
    }]
  }]
}
```

Normalization rules:

- `input` is uncached prompt tokens; `cache_read` and `cache_write` are
  separate. The legacy `tokens_in` total still includes cache (for old
  tables and callers).
- `reasoning` is `null` when the arm does not expose it; do not substitute
  zero.
- `steps` is assistant turns for yaah and the driver's API-call count for
  adapter arms; `null` when unavailable.
- Cost comes from the single shared [pricing.json](pricing.json) table, never
  from an arm's self-reported price. Override the table with `TB_PRICING` and
  the default model (for old runs without model metadata) with `TB_MODEL`.
  Current rates are per million tokens and should be refreshed from the
  provider price sheet.
- `subagent_tokens` is the total child input+output; the detailed split is in
  `usage.sub_tokens_in/out` and `--subs`.

For runs that dispatch sub-agents, the extractor splits the session total into
the orchestrator share and the sub-agent share (`sub_tokens_in/out`, with
`orchestrator = total - sub`). `--subs` prints the per-role breakdown from
`yaah.subagent.usage.v1` trace facts. Orchestration-suite records live in
`../orchestration-tasks/RESULTS.md`.

## Podman gotchas

1. **Short image names**: podman refuses `FROM localstack/localstack:4.3`
   without unqualified-search registries (Docker silently assumes
   docker.io). Fix, user-level, no sudo:
   Merge `unqualified-search-registries = ["docker.io"]` INTO an
   existing ~/.config/containers/registries.conf (creating it only if
   absent) — overwriting the file would drop mirrors, registry
   mappings, or security settings a user configured there.
2. **Upstream task defects at the pin**: see
   [RESULTS.md](RESULTS.md#task-defects-found-at-the-harness-pin-d28711d)
   — fix-git is unbuildable everywhere (deleted fixture repo) and
   png-generation's compose file omits the agent-logs volume (its
   token accounting is lost).
3. **The podman socket can vanish while systemd still reports the unit
   active** — if the SDK fails with FileNotFoundError, restart it:
   `systemctl --user restart podman.socket`.

## Recording results

- Task tables, scores, and caveats: append a new dated section to
  [RESULTS.md](RESULTS.md) — existing sections are never rewritten. This is
  the results ledger.
- [BENCHMARKS.md](../../BENCHMARKS.md) is methodology and comparison context
  only; new run results go in `RESULTS.md`, not that page.
