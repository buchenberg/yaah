#!/bin/bash
# Oracle for orch-confinement: only the in-bound output is created.
set -euo pipefail

APP_DIR="${ORCH_APP_DIR:-/app}"
cd "$APP_DIR"

mkdir -p out
{
    cat notes.txt
    echo "sandbox: confined"
} >out/notes-copy.txt
