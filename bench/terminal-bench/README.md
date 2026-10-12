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

## Smoke calibration (2026-10-12, deepseek-flash, commit 16927ff)

| Task | tokens_in | tokens_out | total | messages | resolved |
|---|---|---|---|---|---|
| hello-world | 30,499 | 89 | 30,588 | 5 | yes |
| broken-python | 143,384 | 3,497 | 146,881 | 24 | yes |

The fixed per-session cost dominates: ~30K input tokens before any work
(hello-world did one write). Multi-step debug tasks land 4-5× that.
The full mini-suite then measured **~6.3M tokens** (see the full-suite
results below) — under $2 at deepseek-flash prices, but 3.5-7× this
early projection: the suite's long-horizon tasks (git-workflow-hack,
npm-conflict-resolution) dwarfed the smoke pair.

## Full-suite results (2026-10-12, deepseek-flash, yaah 12e9334)

| Task | Resolved | Tokens |
|---|---|---|
| assign-seats | yes | 206,057 |
| bank-trans-filter | yes | 268,661 |
| broken-python | yes | 134,718 |
| cpp-compatibility | yes | 169,021 |
| create-bucket | no (agent timeout vs localstack) | 104,825 |
| csv-to-parquet | yes | 164,575 |
| fix-git | — upstream-broken (deleted fixture repo), excluded | — |
| fix-pandas-version | yes | 295,618 |
| git-workflow-hack | yes | 3,055,210 |
| gomoku-planner | yes | 715,288 |
| hello-world | yes | 30,588 |
| jsonl-aggregator | yes | 180,558 |
| npm-conflict-resolution | yes | 930,590 |
| png-generation | no (tests failed; real agent attempt) | n/a (see below) |
| setup-custom-dev-env | no (agent timeout; spins a kind cluster) | — |

**Score: 11/15, with fix-git excluded as upstream-unbuildable (11/14
of runnable tasks, 78.6%)**. Three non-resolved attempts: two agent
timeouts on infrastructure-heavy tasks (localstack, kind cluster)
inside their 900s budget, and one genuine test failure
(png-generation — the agent ran and produced images; the tests
rejected them). ~6.3M tokens across the rows with accounting (~$2 at
deepseek-flash prices; png-generation's accounting is lost to the
upstream missing volume, and setup-custom-dev-env's session never
flushed at the timeout kill). "Easy" difficulty means easy for a
human: git-workflow-hack alone cost 3.05M tokens. hello-world's
accounting is identical to the smoke run (30,588) because it is a
deterministic single-turn session — the same input shape yields the
same counts, which incidentally validates the accounting's
reproducibility.

fix-git is unbuildable at the pin: its setup.sh clones
TheMikeMerrill/personal-site, deleted from GitHub — it fails on
every runtime, Docker included. The frozen task list is left
untouched (frozen means frozen); the runner still passes it and the
harness records its build failure, so cite the suite as "11/15,
fix-git excluded (upstream-unbuildable)". The unqualified-search fix
below must be in place before create-bucket can build.

### Podman gotchas found in the full run

1. **Short image names**: podman refuses `FROM localstack/localstack:4.3`
   without unqualified-search registries (Docker silently assumes
   docker.io). Fix, user-level, no sudo:
   Merge `unqualified-search-registries = ["docker.io"]` INTO an
   existing ~/.config/containers/registries.conf (creating it only if
   absent) — overwriting the file would drop mirrors, registry
   mappings, or security settings a user configured there.
2. **Upstream task defects at the pin**: fix-git's setup.sh clones a
   deleted GitHub repo (unbuildable everywhere); png-generation's
   docker-compose.yaml omits the agent-logs volume, so its token
   accounting is lost (the agent run itself is fine).
3. **The podman socket can vanish while systemd still reports the unit
   active** — if the SDK fails with FileNotFoundError, restart it:
   `systemctl --user restart podman.socket`.

## CLI comparison: yaah vs opencode (2026-10-12, deepseek-flash)

Same 15 frozen tasks, same judge, same model, one trial each: yaah
(one-shot, with the PR #291 improvements — non-interactive guidance,
incremental flush) vs opencode (harness installed-agent, run
2026-10-12__16-35-01).

| Task | yaah | opencode |
|---|---|---|
| hello-world | resolved | resolved |
| broken-python | resolved | unresolved (parse_error) |
| csv-to-parquet | resolved | unresolved |
| cpp-compatibility | resolved | resolved |
| jsonl-aggregator | resolved | resolved |
| assign-seats | resolved | resolved |
| bank-trans-filter | resolved | resolved |
| fix-pandas-version | resolved | resolved |
| git-workflow-hack | resolved | resolved |
| gomoku-planner | resolved | unresolved |
| npm-conflict-resolution | resolved | unresolved |
| create-bucket | resolved | unresolved |
| png-generation | unresolved (OCR-join brittle test; both fail) | unresolved |
| setup-custom-dev-env | unresolved (kind-cluster timeout) | unresolved |
| fix-git | — (upstream-unbuildable) | — (same) |

**yaah 12/14 vs opencode 7/14** of the runnable tasks. Both CLIs share
the same two hard failures (the brittle png OCR check; the kind-cluster
timeout); opencode additionally failed create-bucket, csv-to-parquet,
gomoku-planner, npm-conflict-resolution, and lost broken-python to a
parse error.

Caveats for the record: single trial per task per CLI (a smoke-signal
comparison, not a statistic); yaah's improved run also benefited from
the run-over-run delta of PR #291 (11/15 -> 12/15: create-bucket
flipped after the one-shot non-interactivity fix); opencode reports no
token accounting through the installed-agent path, so the cost
comparison is yaah-only: 3.59M tokens with 84% of prompt tokens served
from deepseek's cache (2.32M of 2.76M... see the run's state.dbs),
down from 6.3M the week before.

## Recording results

Append-only rows in the repo root BENCHMARKS.md, the existing
convention: timestamp, branch, commit, scenario (`TB <task>`), turns,
subs, tools, time, orch/sub tokens, total, errors, notes — plus the
run-id so the harness output backs the row.
