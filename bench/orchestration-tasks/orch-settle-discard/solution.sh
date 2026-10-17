#!/bin/bash
# Oracle for orch-settle-discard: nothing lands; only the answer file exists.
set -euo pipefail

APP_DIR="${ORCH_APP_DIR:-/app}"
cd "$APP_DIR"

printf 'discarded: oracle\n' >answer.txt
rm -f fizzbuzz.py
