#!/bin/bash
# Oracle for orch-settle-discard: nothing lands; only the answer file exists.
set -euo pipefail

# Oracle runs have no model trace; the verifier skips evidence layers for them.
touch /tmp/orch-oracle

APP_DIR="${ORCH_APP_DIR:-/app}"
cd "$APP_DIR"

printf 'discarded: proposal:0:0\n' >answer.txt
rm -f fizzbuzz.py
