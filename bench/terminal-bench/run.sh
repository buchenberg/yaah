#!/bin/bash
# Run the yaah mini-suite (or any task subset) through Terminal-Bench.
#
# Prerequisites (one-time, see README.md):
#   - podman with the user socket: systemctl --user enable --now podman.socket
#   - the docker->podman shim on PATH (podman-compose installed)
#   - a Terminal-Bench checkout with its venv (TB, DATASET)
#   - the yaah static binary (built by this script if absent)
#
# Usage:
#   ./run.sh                          # the frozen 15-task mini-suite
#   ./run.sh hello-world create-bucket  # named tasks (e.g. the 2-task smoke)
#   YAAH_MODEL=deepseek-flash ./run.sh  # override the model
#
# Token budget: each easy task lands in the ~5-30K range on a flash
# model; the full suite is roughly 100-400K tokens. Calibrate on the
# 2-task smoke first and read yaah-profile.txt in the run dir before
# committing to the full suite.

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"

export DOCKER_HOST="${DOCKER_HOST:-unix:///run/user/$(id -u)/podman/podman.sock}"

TB="${TB:-$HOME/Code/Agentic/Frameworks/terminal-bench/.venv/bin/tb}"
DATASET="${DATASET:-$HOME/Code/Agentic/Frameworks/terminal-bench/original-tasks}"

YAAH_BIN="${YAAH_BIN:-/tmp/yaah-bin/yaah}"
export YAAH_BIN
if [ ! -x "$YAAH_BIN" ]; then
  echo "building the static yaah binary..."
  (cd "$REPO_ROOT" && CGO_ENABLED=0 go build -o "$YAAH_BIN" .)
fi

# The provider key: required from the environment. (Do NOT try to read it
# from the host yaah config — its api_key is typically the
# ${DEEPSEEK_API_KEY} interpolation placeholder, not a literal key.)
if [ -z "${DEEPSEEK_API_KEY:-}" ]; then
  echo "error: DEEPSEEK_API_KEY must be exported (the same key your yaah config interpolates)" >&2
  exit 1
fi

if [ "$#" -gt 0 ]; then
  TASKS=("$@")
else
  mapfile -t TASKS < <(grep -v '^#' "$HERE/mini-suite.txt" | awk '{print $1}' | grep -v '^$')
fi
TASK_ARGS=()
for t in "${TASKS[@]}"; do TASK_ARGS+=(-t "$t"); done

export PYTHONPATH="$HERE${PYTHONPATH:+:$PYTHONPATH}"

exec "$TB" run \
  --agent-import-path "yaah_agent:YaahAgent" \
  "${TASK_ARGS[@]}" \
  --dataset-path "$DATASET" \
  --output-path "${OUTPUT_PATH:-$HOME/Code/Agentic/Frameworks/terminal-bench/runs}" \
  --n-concurrent "${N_CONCURRENT:-1}"
