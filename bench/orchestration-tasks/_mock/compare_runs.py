#!/usr/bin/env python3
"""Compare two mock-provider runs for structural determinism.

Timestamps and runtime-generated proposal ids are allowed to differ. The
things that must match are the verifier verdict, the message roles/tool
sequence, the token accounting, and the trace record-schema counts.
"""

from __future__ import annotations

import argparse
import glob
import json
import sqlite3
import sys
from pathlib import Path
from typing import Any


def _one(pattern: str) -> str:
    matches = glob.glob(pattern, recursive=True)
    if len(matches) != 1:
        raise SystemExit(f"expected one match for {pattern}, got {matches}")
    return matches[0]


def summarize(run_dir: Path) -> dict[str, Any]:
    results = json.loads((run_dir / "results.json").read_text())["results"]
    if len(results) != 1:
        raise SystemExit(f"{run_dir}: expected one trial, got {len(results)}")
    trial = results[0]
    state = sqlite3.connect(
        _one(str(run_dir / "**" / "state.db"))
    )
    try:
        tokens = list(
            state.execute(
                "SELECT tokens_in, tokens_out, tokens_cached, "
                "sub_tokens_in, sub_tokens_out FROM sessions"
            )
        )
        messages = list(
            state.execute(
                "SELECT role, tool_name FROM messages ORDER BY idx"
            )
        )
    finally:
        state.close()

    trace = sqlite3.connect(
        _one(str(run_dir / "**" / "trace.sqlite"))
    )
    try:
        schemas = dict(
            trace.execute(
                "SELECT schema_ref, COUNT(*) FROM records "
                "GROUP BY schema_ref ORDER BY schema_ref"
            )
        )
    finally:
        trace.close()

    return {
        "resolved": trial["is_resolved"],
        "parser_results": trial["parser_results"],
        "tokens": tokens,
        "messages": messages,
        "schemas": schemas,
    }


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("run_a", type=Path)
    parser.add_argument("run_b", type=Path)
    args = parser.parse_args()

    a, b = summarize(args.run_a), summarize(args.run_b)
    if a == b:
        print("deterministic: verdict, message sequence, tokens, and schemas match")
        return
    print("MISMATCH", file=sys.stderr)
    for key in sorted(a):
        if a[key] != b[key]:
            print(f"\n{key}:\n  A={a[key]!r}\n  B={b[key]!r}", file=sys.stderr)
    raise SystemExit(1)


if __name__ == "__main__":
    main()
