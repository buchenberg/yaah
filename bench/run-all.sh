#!/bin/bash
# Umbrella runner: coding suite + orchestration suite, two scoreboards.
#
# The task sets stay separate on purpose. The coding suite measures outcome
# accuracy and cost; the orchestration suite measures trace compliance
# (delegation, settlement, supervision) as well as end state. Mixing them into
# one resolved-% would hide which property regressed.
#
# Usage:
#   ./bench/run-all.sh                  # both suites, one trial
#   SUITES=orch ./bench/run-all.sh      # orchestration only
#   SUITES=coding ./bench/run-all.sh    # coding only
#   N_CONCURRENT=2 OUTPUT_PATH=/tmp/runs ./bench/run-all.sh
#
# Env:
#   CODING_DATASET  terminal-bench dataset root (default: the checkout's
#                   original-tasks)
#   CODING_SUITE    coding task list (default: bench/terminal-bench/suite.txt)
#   ORCH_DATASET    orchestration dataset root (default: bench/orchestration-tasks)
#   ORCH_SUITE      orchestration task list (default: <ORCH_DATASET>/suite.txt)
#   OUTPUT_PATH     run output root shared by both suites
#
# Writes <OUTPUT_PATH>/<run-id>/metrics.json for each run and a combined
# summary-<UTC timestamp>.txt with tokens split into orchestrator and
# sub-agent shares.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"

SUITES="${SUITES:-coding orch}"
N_CONCURRENT="${N_CONCURRENT:-1}"
OUTPUT_PATH="${OUTPUT_PATH:-$HOME/Code/Agentic/Frameworks/terminal-bench/runs}"
CODING_DATASET="${CODING_DATASET:-$HOME/Code/Agentic/Frameworks/terminal-bench/original-tasks}"
CODING_SUITE="${CODING_SUITE:-$HERE/terminal-bench/suite.txt}"
ORCH_DATASET="${ORCH_DATASET:-$REPO_ROOT/bench/orchestration-tasks}"
ORCH_SUITE="${ORCH_SUITE:-$ORCH_DATASET/suite.txt}"
EXTRACT="$HERE/terminal-bench/extract_metrics.py"

mkdir -p "$OUTPUT_PATH"
STAMP="$(date -u +%Y-%m-%d__%H-%M-%S)"
SUMMARY="$OUTPUT_PATH/summary-$STAMP.txt"
RUN_IDS=()

run_suite() {
	local name="$1" dataset="$2" suite="$3"
	echo "=== $name suite"
	echo "    dataset: $dataset"
	echo "    suite:   $suite"
	local log
	log="$(mktemp)"
	DATASET="$dataset" SUITE="$suite" OUTPUT_PATH="$OUTPUT_PATH" \
		N_CONCURRENT="$N_CONCURRENT" "$HERE/terminal-bench/run.sh" 2>&1 | tee "$log"
	local run_id
	run_id="$(awk '/^Run ID:/ {print $3; exit}' "$log")"
	rm -f "$log"
	if [ -z "$run_id" ]; then
		echo "error: $name suite produced no run id" >&2
		return 1
	fi
	RUN_IDS+=("$name:$run_id")
	python3 "$EXTRACT" --json "$OUTPUT_PATH" "$run_id" \
		>"$OUTPUT_PATH/$run_id/metrics.json"
}

for suite in $SUITES; do
	case "$suite" in
	coding) run_suite coding "$CODING_DATASET" "$CODING_SUITE" ;;
	orch) run_suite orch "$ORCH_DATASET" "$ORCH_SUITE" ;;
	*)
		echo "error: unknown suite $suite (use coding, orch, or both)" >&2
		exit 2
		;;
	esac
done

{
	echo "Combined benchmark summary"
	echo "generated: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
	echo
	for entry in "${RUN_IDS[@]}"; do
		name="${entry%%:*}"
		run_id="${entry##*:}"
		echo "## $name ($run_id)"
		python3 "$EXTRACT" --subs "$OUTPUT_PATH" "$run_id"
		echo
	done
} | tee "$SUMMARY"

echo
echo "summary:  $SUMMARY"
for entry in "${RUN_IDS[@]}"; do
	name="${entry%%:*}"
	run_id="${entry##*:}"
	echo "$name metrics: $OUTPUT_PATH/$run_id/metrics.json"
done
