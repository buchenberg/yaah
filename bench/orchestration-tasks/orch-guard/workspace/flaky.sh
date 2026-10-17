#!/bin/sh
# Fails four times, then succeeds once. Attempt counter lives outside /app.
STATE="${FLAKY_STATE:-/tmp/flaky.count}"
OUT="${ORCH_APP_DIR:-/app}/ok.txt"
# A delegate re-dispatch after success must not advance or rewrite the marker:
# the converged attempt number is part of the evidence.
if [ -f "$OUT" ]; then
    echo "already succeeded"
    exit 0
fi
n=$(cat "$STATE" 2>/dev/null || echo 0)
n=$((n + 1))
printf '%s\n' "$n" >"$STATE"
if [ "$n" -lt 5 ]; then
    echo "flaky failure $n" >&2
    exit 1
fi
echo "attempts=$n" >"$OUT"
echo "flaky success on attempt $n"
