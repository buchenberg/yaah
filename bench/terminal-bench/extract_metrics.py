#!/usr/bin/env python3
"""Extract per-task metrics from Terminal-Bench run directories.

Prints verdict, agent wall time, and token accounting for each task in
a run — the same numbers recorded in RESULTS.md. Accounting sources:
yaah sums its in-container state.db (sessions table, tokens_in
includes cached); hermes and aider read their usage-report JSONs,
whose input_tokens exclude (hermes) or include (aider) cached
buckets — normalized to yaah semantics via each report's
prompt_includes_cache flag. opencode reports nothing through the
installed-agent path.

Usage:
    python3 extract_metrics.py RUNS_DIR RUN_ID [RUN_ID ...]

RUNS_DIR is the terminal-bench `runs/` output directory (default per
run.sh: ~/Code/Agentic/Frameworks/terminal-bench/runs).
"""

import datetime
import glob
import json
import sqlite3
import sys


def agent_seconds(result):
    try:
        start = datetime.datetime.fromisoformat(result["agent_started_at"])
        end = datetime.datetime.fromisoformat(result["agent_ended_at"])
        return int((end - start).total_seconds())
    except (KeyError, TypeError, ValueError):
        return None


def yaah_tokens(runs_dir, run_id, task_id):
    path = glob.glob(
        f"{runs_dir}/{run_id}/{task_id}/*/agent-logs/yaah-home/state.db"
    )
    if not path:
        return None
    con = sqlite3.connect(path[0])
    try:
        row = con.execute(
            "SELECT COALESCE(SUM(tokens_in),0), COALESCE(SUM(tokens_out),0),"
            " COALESCE(SUM(tokens_cached),0), COUNT(*) FROM sessions"
        ).fetchone()
    finally:
        con.close()
    return row


def usage_report_tokens(runs_dir, run_id, task_id):
    """Read a driver usage report (hermes-usage.json or aider-usage.json)
    and normalize to yaah semantics: prompt includes cached tokens.

    hermes reports input_tokens WITHOUT cache reads/writes; aider reports
    input_tokens WITH cache hits (deepseek usage semantics). The aider
    report carries prompt_includes_cache=true to disambiguate. A
    truncated/invalid report degrades to "no accounting" for this task
    rather than aborting the run summary; a report is written even for
    failed/partial runs, so a parsed report counts as accounting
    regardless of `completed`.
    """
    for name in ("hermes-usage.json", "aider-usage.json"):
        path = glob.glob(
            f"{runs_dir}/{run_id}/{task_id}/*/agent-logs/{name}"
        )
        if not path:
            continue
        try:
            with open(path[0]) as f:
                report = json.load(f)
            if not isinstance(report, dict):
                return None
        except (OSError, ValueError):
            return None
        prompt = report.get("input_tokens") or 0
        cache_read = report.get("cache_read_tokens") or 0
        cache_write = report.get("cache_write_tokens") or 0
        if not report.get("prompt_includes_cache"):
            prompt += cache_read + cache_write
        return (prompt, report.get("output_tokens") or 0, cache_read, 1)
    return None


def main(argv):
    if len(argv) < 3:
        print(__doc__)
        return 2
    runs_dir, run_ids = argv[1], argv[2:]
    totals = {run_id: [0, 0, 0, 0] for run_id in run_ids}  # in, out, cached, n
    for run_id in run_ids:
        with open(f"{runs_dir}/{run_id}/results.json") as f:
            results = json.load(f)["results"]
        print(f"=== {run_id}")
        for r in sorted(results, key=lambda x: x["task_id"]):
            secs = agent_seconds(r)
            line = (
                f"{r['task_id']:24s} resolved={str(r['is_resolved']):5s} "
                f"time={secs if secs is not None else '?':>5}"
            )
            tk = yaah_tokens(runs_dir, run_id, r["task_id"]) or usage_report_tokens(
                runs_dir, run_id, r["task_id"]
            )
            if tk and tk[3]:
                tokens_in, tokens_out, cached, _ = tk
                line += (
                    f" in={tokens_in:>8} out={tokens_out:>7} "
                    f"cached={cached:>8}"
                )
                totals[run_id][0] += tokens_in
                totals[run_id][1] += tokens_out
                totals[run_id][2] += cached
                totals[run_id][3] += 1
            print(line)
        print()
    for run_id, (t_in, t_out, cached, n) in totals.items():
        if n:
            pct = 100 * cached / t_in if t_in else 0
            print(
                f"{run_id}: {n} tasks with accounting, "
                f"in={t_in:,} out={t_out:,} cached={cached:,} "
                f"({pct:.0f}% of prompt tokens)"
            )
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
