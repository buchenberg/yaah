#!/bin/sh
# Vendor the shared verifier library into each task's tests/ directory.
#
# Terminal-Bench exposes only the task's own tests directory as $TEST_DIR, so
# a suite-level _lib is not importable inside the task container. Run this
# after editing _lib/orch_assert.py. run.sh calls it automatically when DATASET
# points at this directory.
set -eu

HERE="$(cd "$(dirname "$0")" && pwd)"

for task in "$HERE"/*/; do
	[ -f "$task/task.yaml" ] || continue
	mkdir -p "$task/tests/_lib"
	cp "$HERE/_lib/orch_assert.py" "$task/tests/_lib/orch_assert.py"
	: >"$task/tests/_lib/__init__.py"
done
