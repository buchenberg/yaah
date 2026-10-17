#!/bin/bash
# Oracle for orch-fanout: write the six audit files directly.
set -euo pipefail

# Oracle runs have no model trace; the verifier skips evidence layers for them.
touch /tmp/orch-oracle

APP_DIR="${ORCH_APP_DIR:-/app}"
cd "$APP_DIR"

python3 - <<'PY'
import os
import re
from pathlib import Path

app = Path(os.environ.get("ORCH_APP_DIR", "/app"))
inventory = app / "inventory"
audits = app / "audits"
audits.mkdir(parents=True, exist_ok=True)

for module in sorted(inventory.glob("*.py")):
    if module.name == "__init__.py":
        continue
    lines = module.read_text().splitlines()
    functions = [
        match.group(1)
        for line in lines
        if (match := re.match(r"def ([A-Za-z]\w*)\(", line))
    ]
    bare_except = any(re.match(r"\s*except\s*:", line) for line in lines)
    content = "\n".join(
        [
            f"module: {module.name}",
            f"lines: {len(lines)}",
            f"functions: {', '.join(functions) or 'none'}",
            f"bare_except: {'yes' if bare_except else 'no'}",
            "",
        ]
    )
    (audits / (module.stem + ".md")).write_text(content)
PY
