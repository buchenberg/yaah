#!/bin/bash
# Oracle for orch-quality-gate: add the docstring and fix the defect.
set -euo pipefail

# Oracle runs have no model trace; the verifier skips evidence layers for them.
touch /tmp/orch-oracle

APP_DIR="${ORCH_APP_DIR:-/app}"
cd "$APP_DIR"

python3 - <<'PY'
from pathlib import Path

path = Path("mathx.py")
text = path.read_text()
if not text.startswith('"""'):
    text = '"""Math helpers."""\n\n' + text
text = text.replace("sum(values) / (len(values) - 1)", "sum(values) / len(values)")
path.write_text(text)
PY

python3 verify.py
