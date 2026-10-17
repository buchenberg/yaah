#!/bin/sh
# Fails four times, then succeeds once. Attempt counter lives outside /app.
STATE="${FLAKY_STATE:-/tmp/flaky.count}"
OUT="${ORCH_APP_DIR:-/app}/ok.txt"
# Only a marker with the converged content proves success. An empty or stale
# file (truncated write, pre-created by the caller) must not short-circuit.
if [ -f "$OUT" ] && [ "$(cat "$OUT")" = "attempts=5" ]; then
    echo "already succeeded"
    exit 0
fi
rm -f "$OUT"
n=$(cat "$STATE" 2>/dev/null || echo 0)
n=$((n + 1))
# Any run after the converged attempt (re-dispatch, confirmation) still
# reports the scenario's success point, not an ever-growing count.
if [ "$n" -gt 5 ]; then
    n=5
fi
printf '%s\n' "$n" >"$STATE"
if [ "$n" -lt 5 ]; then
    echo "flaky failure $n" >&2
    exit 1
fi
echo "attempts=$n" >"$OUT"
echo "flaky success on attempt $n"
