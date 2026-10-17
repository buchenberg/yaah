#!/bin/sh
# Fails four times, then succeeds once. Attempt counter lives outside /app.
STATE="${FLAKY_STATE:-/tmp/flaky.count}"
n=$(cat "$STATE" 2>/dev/null || echo 0)
n=$((n + 1))
printf '%s\n' "$n" >"$STATE"
if [ "$n" -lt 5 ]; then
    echo "flaky failure $n" >&2
    exit 1
fi
echo "attempts=$n" >"${ORCH_APP_DIR:-/app}/ok.txt"
echo "flaky success on attempt $n"
