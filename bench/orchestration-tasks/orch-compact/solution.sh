#!/bin/bash
# Oracle for orch-compact: produce the report directly.
set -euo pipefail

APP_DIR="${ORCH_APP_DIR:-/app}"
cd "$APP_DIR"

python3 - <<'PY'
from pathlib import Path

lines = []
for path in sorted(Path("data").glob("*.txt")):
    first = path.read_text().split()[0]
    lines.append(f"{path.name}: {first}")
Path("report.txt").write_text("\n".join(lines) + "\n")
PY
