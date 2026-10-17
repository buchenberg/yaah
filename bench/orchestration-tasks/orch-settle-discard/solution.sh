#!/bin/bash
# Oracle for orch-settle-discard: nothing lands; only the answer file exists.
set -euo pipefail

APP_DIR="${ORCH_APP_DIR:-/app}"
cd "$APP_DIR"

printf 'discarded: proposal:0:0\n' >answer.txt
rm -f fizzbuzz.py
