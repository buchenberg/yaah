# Terminal-Bench mini-suite (yaah)

A frozen 15-task Terminal-Bench subset for CLI-vs-CLI comparison at
flash-model prices, plus the yaah agent adapter that plugs yaah into
the Terminal-Bench harness as an installed agent.

- Tasks: `mini-suite.txt` (frozen — do not edit; extend with a new file)
- Agent adapter: `yaah_agent.py` (import path `yaah_agent:YaahAgent`)
- Container setup: `yaah-setup.sh.j2` (config written in-container)
- Runner: `run.sh`

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
./run.sh                          # the frozen 15-task mini-suite
./run.sh hello-world broken-python  # the 2-task smoke
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

## Smoke calibration (2026-10-13, deepseek-flash, commit 16927ff)

| Task | tokens_in | tokens_out | total | messages | resolved |
|---|---|---|---|---|---|
| hello-world | 30,499 | 89 | 30,588 | 5 | yes |
| broken-python | 143,384 | 3,497 | 146,881 | 24 | yes |

The fixed per-session cost dominates: ~30K input tokens before any work
(hello-world did one write). Multi-step debug tasks land 4-5× that.
Projected full mini-suite: **~0.9-1.8M tokens** — under a dollar at
deepseek-flash prices. (Input-heavy: ~97% input. A context-window cap or
compaction tuning on longer tasks would move this number down.)

## Recording results

Append-only rows in the repo root BENCHMARKS.md, the existing
convention: timestamp, branch, commit, scenario (`TB <task>`), turns,
subs, tools, time, orch/sub tokens, total, errors, notes — plus the
run-id so the harness output backs the row.
