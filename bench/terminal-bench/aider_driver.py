#!/usr/bin/env python3
"""Headless aider driver for Terminal-Bench.

Runs one instruction through aider's Coder library — the same flow as
`aider --message <msg> --yes` — and dumps exact token accounting to a
JSON report, since aider's own usage reporting is display-only.

Semantics (per aider's base_coder comment on deepseek):
prompt_cache_hit_tokens + prompt_cache_miss_tokens == prompt_tokens,
so the report's input_tokens ALREADY INCLUDES cache hits — matching
yaah's tokens_in. cache reads/writes are reported separately for the
cache-hit share. The hook wraps calculate_and_show_tokens_and_cost,
which every LLM call to the main coder passes through. The driver
runs with stream=False (a standard aider mode): on the streaming
path aider skips the final usage-only chunk (empty choices), so its
per-call "Tokens:" numbers are estimates there; non-streaming gives
real completion.usage. A short autonomy preamble is prepended to the
instruction — without it aider (interactive by design) answers
one-shot instructions with clarifying questions instead of doing the
work; this mirrors yaah's one-shot guidance and the other CLIs'
built-in autonomous defaults.

Usage:
    python3 aider_driver.py --message "..." [--model deepseek/deepseek-chat]
                            [--usage-file /agent-logs/aider-usage.json]

The default model name is the one aider's own model-settings.yml
curates for this family (edit_format: diff, caches_by_default: true,
repo map); deepseek-chat is the flash tier's wire alias on
api.deepseek.com — the same model the other CLIs in this comparison
bench.
"""

import argparse
import json
import os
import sys


PREAMBLE = """You are running non-interactively as a benchmark agent.
Nobody will answer questions or provide more information, so do not
ask for either. Inspect the environment yourself, decide what to do,
make the required changes, and verify them on your own. Complete the
entire task in this single response.
"""


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--message", required=True)
    parser.add_argument(
        "--model", default="deepseek/deepseek-chat"
    )
    parser.add_argument(
        "--usage-file", default="/agent-logs/aider-usage.json"
    )
    args = parser.parse_args()

    from aider.coders import Coder
    from aider.io import InputOutput
    from aider.models import Model

    io = InputOutput(False, True)  # pretty=False, yes_always=True
    coder = Coder.create(
        main_model=Model(args.model), io=io, stream=False
    )

    totals = {
        "input_tokens": 0,
        "output_tokens": 0,
        "cache_read_tokens": 0,
        "cache_write_tokens": 0,
        "api_calls": 0,
    }
    wrapped = coder.calculate_and_show_tokens_and_cost

    def hook(messages, completion=None):
        usage = getattr(completion, "usage", None)
        if usage is not None:
            totals["input_tokens"] += getattr(usage, "prompt_tokens", 0) or 0
            totals["output_tokens"] += (
                getattr(usage, "completion_tokens", 0) or 0
            )
            totals["cache_read_tokens"] += (
                getattr(usage, "prompt_cache_hit_tokens", 0)
                or getattr(usage, "cache_read_input_tokens", 0)
                or 0
            )
            totals["cache_write_tokens"] += (
                getattr(usage, "cache_creation_input_tokens", 0) or 0
            )
            totals["api_calls"] += 1
        return wrapped(messages, completion)

    coder.calculate_and_show_tokens_and_cost = hook

    completed = True
    error = None
    try:
        coder.run(with_message=PREAMBLE + args.message)
    except Exception as exc:  # noqa: BLE001 — report any failure verbatim
        completed = False
        error = f"{type(exc).__name__}: {exc}"
        print(f"aider_driver: run failed: {error}", file=sys.stderr)

    report = {
        **totals,
        "model": args.model,
        "completed": completed,
        "failed": not completed,
        "error": error,
        "prompt_includes_cache": True,
    }
    try:
        os.makedirs(os.path.dirname(args.usage_file), exist_ok=True)
        with open(args.usage_file, "w") as f:
            json.dump(report, f, indent=2)
    except OSError as exc:
        print(f"aider_driver: usage report not written: {exc}", file=sys.stderr)
    return 0 if completed else 1


if __name__ == "__main__":
    sys.exit(main())
