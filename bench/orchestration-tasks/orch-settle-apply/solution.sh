#!/bin/bash
# Oracle for orch-settle-apply: apply both changes directly.
set -euo pipefail

APP_DIR="${ORCH_APP_DIR:-/app}"
cd "$APP_DIR"

printf -- "- v2: power helper\n" >>CHANGELOG.md

python3 - <<'PY'
from pathlib import Path

calc = Path("calc.py")
source = calc.read_text()
if "def power(" not in source:
    source += "\n\ndef power(base, exp):\n    return base**exp\n"
    calc.write_text(source)

verify = Path("verify.py")
if "power(2, 5)" not in verify.read_text():
    text = verify.read_text()
    text = text.replace(
        "from calc import add, multiply\n",
        "from calc import add, multiply, power\n",
    )
    text = text.replace(
        "assert multiply(3, 4) == 12\n",
        "assert multiply(3, 4) == 12\nassert power(2, 5) == 32\n",
    )
    verify.write_text(text)
PY

python3 verify.py
