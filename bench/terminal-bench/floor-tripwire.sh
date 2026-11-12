#!/bin/bash
# Floor tripwire (yaah-6mi9.3): the hello-world prompt budget as a
# pinned instrument.
#
# The fixed per-CALL prompt floor is the belt's cost signature: every
# tool's schema rides every request. The floor already has a
# deterministic instrument — hello-world at 3 calls, one minute,
# flash-tier pennies — but no tripwire, so the file-tools era doubled
# it (10.5K to 20.7K tokens/call) silently between runs. This script
# builds the tree fresh, runs exactly that instrument, and fails
# closed when the measured per-call floor exceeds FLOOR_BUDGET.
#
# The budget is the post-curation baseline (yaah-6mi9.1/6mi9.2:
# 13,482-13,506 tokens/call over three measurements) plus ~15% for
# prompt-text noise: 15,500 tokens/call. A failure is not a flake —
# something changed the shape of every call (a belt growth, a prompt
# reorganization, a provider cache shift). Name the cause in the bead
# evidence, then either justify the new floor by updating the budget
# here and the floor curves in HISTORY.md, or trim what grew.
#
# Prerequisites are run.sh's (podman user socket, TB venv,
# DEEPSEEK_API_KEY exported).
#
#   ./floor-tripwire.sh          # build, run hello-world, assert the floor

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"

# The pinned per-call budget, in prompt-in tokens (cache reads
# included — they are prompt-in cost even when billed at a discount).
FLOOR_BUDGET_PER_CALL=15500

export DOCKER_HOST="${DOCKER_HOST:-unix:///run/user/$(id -u)/podman/podman.sock}"

RUNS="$(mktemp -d /tmp/yaah-floor-tripwire.XXXXXX)"
trap 'rm -rf "$RUNS"' EXIT

# Build fresh from the current tree: the tripwire must measure what is
# about to ship, not a binary left in /tmp from an earlier change.
YAAH_BIN="$RUNS/yaah" && export YAAH_BIN
(cd "$REPO_ROOT" && CGO_ENABLED=0 go build -trimpath -o "$YAAH_BIN" .)

# Run the instrument: one hello-world trial, nothing else.
(cd "$HERE" && OUTPUT_PATH="$RUNS" ./run.sh hello-world)

RUN_ID="$(ls "$RUNS" | head -1)"

# Extract the trial's accounting, assert the floor. The failure output
# names the pinned budget, the measured floor, and the choice to make.
python3 - "$HERE/extract_metrics.py" "$RUNS" "$RUN_ID" \
  "$FLOOR_BUDGET_PER_CALL" <<'EOF'
import json, subprocess, sys

metrics, runs_dir, run_id, budget = sys.argv[1:5]
data = json.loads(subprocess.run(
    ["python3", metrics, "--json", runs_dir, run_id],
    capture_output=True, text=True, check=True).stdout)

task = next((t for t in data["runs"][0]["tasks"] if t["task_id"] == "hello-world"), None)
if task is None:
    sys.exit("floor tripwire: hello-world produced no accounting row — the run did not land")
if not task["resolved"]:
    sys.exit("floor tripwire: hello-world did not resolve — a failed instrument measures nothing")
calls = task["steps"]
if calls <= 0:
    sys.exit("floor tripwire: hello-world recorded zero calls")

prompt_in = task["tokens"]["input"] + task["tokens"]["cache_read"]
per_call = prompt_in / calls
budget = float(budget)

measured = f"{per_call:,.0f} prompt-in/call ({prompt_in:,} tokens over {calls} calls)"
if per_call > budget:
    sys.exit(
        f"floor tripwire: FAIL — measured {measured}, "
        f"budget {budget:,.0f}/call (post-curation baseline ~13.5K plus 15%). "
        "Something changed the shape of every call. Justify the new floor "
        "(update FLOOR_BUDGET_PER_CALL and the floor curves in HISTORY.md, "
        "citing the cause on yaah-6mi9.3) or trim what grew."
    )
print(f"floor tripwire: OK — {measured}, budget {budget:,.0f}/call")
EOF
