# Terminal-Bench suite (yaah)

A small Terminal-Bench task subset for CLI-vs-CLI comparison at
flash-model prices, plus the yaah agent adapter that plugs yaah into
the Terminal-Bench harness as an installed agent.

- Tasks: `suite.txt` (6 tasks, editable — unfrozen; cite the file's
  commit alongside any score. The frozen 15- and 12-task lists live
  in git history; recorded runs cite their pinned commits)
- Agent adapter: `yaah_agent.py` (import path `yaah_agent:YaahAgent`)
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
SUITE=<file> ./run.sh             # any task-list file in this directory
YAAH_MODEL=<model> ./run.sh       # any tier you like; default deepseek-flash
```

The runner builds the static yaah binary if `$YAAH_BIN` is absent, reads
the API key from the environment (never from the yaah config — its
`api_key` is typically the `${DEEPSEEK_API_KEY}` placeholder), and runs
the harness with the agent adapter on `PYTHONPATH`.

Model config inside the task container: `agents.default.model` and
`small_model` are the model (default deepseek-flash); sub-agents inherit
it (`small_model` is compaction-only). `approval: allow` — benchmark
sessions are non-interactive; the task container is the sandbox, same as
every other CLI agent benched here.

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

Per-task metrics (verdict, agent wall time, yaah token accounting
summed from the state.dbs) for one or more runs:

```bash
python3 extract_metrics.py ~/Code/Agentic/Frameworks/terminal-bench/runs <run-id> [run-id ...]
```

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
  [RESULTS.md](RESULTS.md) — existing sections are never rewritten.
- Ledger: append-only rows in the repo root
  [BENCHMARKS.md](../../BENCHMARKS.md), the existing
  convention: timestamp, branch, commit, scenario (`TB <task>`), turns,
  subs, tools, time, orch/sub tokens, total, errors, notes — plus the
  run-id so the harness output backs the row.
