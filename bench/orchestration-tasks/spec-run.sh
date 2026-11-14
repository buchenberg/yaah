#!/usr/bin/env bash
# Run one orchestration bench task from its checked-in workflow spec.
#
# The spec IS the dispatcher (bead yaah-0u0v.18): there is no per-task Go code,
# no hand-authored orchestration script, and no harness change. This driver
# only drives the existing CLI in the order the bench needs it, then hands the
# run's trace to the verifier:
#
#   yaah workflow validate <spec>                     model-free gate
#   yaah workflow run <spec> --state --artifact-root  the run
#   yaah workflow explain --from-trace <run> --json   the trace report
#   python3 _lib/orch_trace.py --trace trace.json     the assertions
#
# Usage:
#   bench/orchestration-tasks/spec-run.sh [task]     # default: orch-fanout
#
# Environment:
#   ORCH_APP_DIR   workspace root the artifacts are written under (default /app)
#   ORCH_OUT_DIR   where the state db and trace json go (default <task>/.spec-run)
#   YAAH           the binary to drive (default: yaah on PATH)
#
# See SPEC-DRIVEN.md for what each step proves and why `run` needs a configured
# sub-agent runner.
set -euo pipefail

task="${1:-orch-fanout}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
spec="$here/$task/spec/workflow.yaml"
app="${ORCH_APP_DIR:-/app}"
out="${ORCH_OUT_DIR:-$here/$task/.spec-run}"
yaah="${YAAH:-yaah}"
run_id="bench-$task"

if [ ! -f "$spec" ]; then
  echo "no workflow spec for task '$task': $spec" >&2
  echo "tasks with a spec: $(ls -d "$here"/*/spec/workflow.yaml 2>/dev/null | cut -d/ -f1 | xargs -n1 basename 2>/dev/null | tr '\n' ' ')" >&2
  exit 2
fi

mkdir -p "$out"

echo "== validate: $spec"
"$yaah" workflow validate "$spec"

echo "== run: $spec (artifacts under $app, state in $out)"
"$yaah" workflow run "$spec" --run-id "$run_id" --state "$out/state.db" \
  --artifact-root "$app" --json | tee "$out/run.json"

echo "== trace: $run_id"
"$yaah" workflow explain --from-trace "$run_id" --state "$out/state.db" \
  --json "$spec" >"$out/trace.json"

echo "== assert: $out/trace.json"
python3 "$here/_lib/orch_trace.py" --trace "$out/trace.json" --task "$task" \
  --artifact-root "$app"

echo "OK: $task ran from $spec; report in $out"
