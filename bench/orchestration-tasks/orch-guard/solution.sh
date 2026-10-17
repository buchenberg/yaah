#!/bin/bash
# Oracle for orch-guard: drive flaky.sh to success and report the warning.
set -euo pipefail

# Oracle runs have no model trace; the verifier skips evidence layers for them.
touch /tmp/orch-oracle

APP_DIR="${ORCH_APP_DIR:-/app}"
cd "$APP_DIR"

rm -f /tmp/flaky.count /app/ok.txt
attempts=0
until ./flaky.sh >/dev/null 2>&1; do
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 8 ]; then
        echo "flaky.sh never succeeded" >&2
        exit 1
    fi
done
attempts=$((attempts + 1))

cat >answer.txt <<EOF
attempts: $attempts
WARNING: error rate 100% (4/4 recent calls). Consider a different approach.
EOF
