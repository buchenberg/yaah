#!/usr/bin/env python3
"""Extract per-task metrics from Terminal-Bench run directories.

Prints verdict, agent wall time, and token accounting for each task in a
run — the same numbers recorded in RESULTS.md. Accounting sources:
yaah sums its in-container state.db (sessions table, tokens_in includes
cached); hermes and aider read their usage-report JSONs, whose
input_tokens exclude (hermes) or include (aider) cached buckets —
normalized to yaah semantics via each report's prompt_includes_cache
flag. opencode reports nothing through the installed-agent path.

For yaah runs with sub-agents, the session totals are split into the
orchestrator share and the sub-agent share. `finishSubAgent` folds child
usage into the parent totals while also recording it in
`sub_tokens_in/out`, so:

    orchestrator = session_total - sub_agents

`--subs` additionally prints the per-role breakdown from the shepherd
trace store: one `yaah.subagent.usage.v1` fact per dispatch (older runs
fall back to the final `yaah.execution.completed.v1` turn per sub-session,
which undercounts multi-turn children). `--json` emits the same data for
the umbrella runner.

Usage:
    python3 extract_metrics.py [--subs] [--json] RUNS_DIR RUN_ID [RUN_ID ...]

RUNS_DIR is the terminal-bench `runs/` output directory.
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


def yaah_usage(runs_dir, run_id, task_id):
    """Return session-level yaah usage, including the sub-agent split."""
    paths = glob.glob(
        f"{runs_dir}/{run_id}/{task_id}/*/agent-logs/yaah-home/state.db"
    )
    if not paths:
        return None
    con = sqlite3.connect(paths[0])
    try:
        try:
            row = con.execute(
                "SELECT COALESCE(SUM(tokens_in),0), COALESCE(SUM(tokens_out),0),"
                " COALESCE(SUM(tokens_cached),0),"
                " COALESCE(SUM(sub_tokens_in),0), COALESCE(SUM(sub_tokens_out),0),"
                " COUNT(*) FROM sessions"
            ).fetchone()
        except sqlite3.OperationalError:
            # Older session rows predate the sub_tokens_* columns.
            row = con.execute(
                "SELECT COALESCE(SUM(tokens_in),0), COALESCE(SUM(tokens_out),0),"
                " COALESCE(SUM(tokens_cached),0), 0, 0, COUNT(*) FROM sessions"
            ).fetchone()
    finally:
        con.close()
    tokens_in, tokens_out, cached, sub_in, sub_out, sessions = row
    return {
        "tokens_in": tokens_in,
        "tokens_out": tokens_out,
        "tokens_cached": cached,
        "sub_tokens_in": sub_in,
        "sub_tokens_out": sub_out,
        "orch_tokens_in": max(tokens_in - sub_in, 0),
        "orch_tokens_out": max(tokens_out - sub_out, 0),
        "sessions": sessions,
    }


def sub_session_usage(runs_dir, run_id, task_id):
    """Per-role sub-agent token totals from the shepherd trace store.

    Current runs record one ``yaah.subagent.usage.v1`` capture per dispatch
    with the child's cumulative usage. Older runs only have the final
    ``yaah.execution.completed.v1`` turn per sub-session, which undercounts
    multi-turn children; those are reported as a fallback with a note.
    """
    paths = glob.glob(
        f"{runs_dir}/{run_id}/{task_id}/*/agent-logs/yaah-traces/trace.sqlite"
    )
    if not paths:
        return [], "none"
    try:
        con = sqlite3.connect(f"file:{paths[0]}?mode=ro", uri=True)
    except sqlite3.Error:
        return [], "none"
    try:
        rows = con.execute(
            """
            SELECT json_extract(body_json, '$.role'),
                   json_extract(body_json, '$.model'),
                   COALESCE(SUM(json_extract(body_json, '$.prompt_tokens')), 0),
                   COALESCE(SUM(json_extract(body_json, '$.completion_tokens')), 0),
                   COUNT(*)
            FROM records
            WHERE schema_ref = 'yaah.subagent.usage.v1'
            GROUP BY 1, 2
            ORDER BY 1
            """
        ).fetchall()
        if rows:
            return [
                {
                    "role": role or "?",
                    "model": model or "?",
                    "tokens_in": int(prompt or 0),
                    "tokens_out": int(completion or 0),
                    "dispatches": count,
                }
                for role, model, prompt, completion, count in rows
            ], "usage-facts"
        rows = con.execute(
            """
            SELECT json_extract(body_json, '$.session_id'),
                   COALESCE(SUM(json_extract(body_json, '$.prompt_tokens')), 0),
                   COALESCE(SUM(json_extract(body_json, '$.completion_tokens')), 0),
                   COUNT(*)
            FROM records
            WHERE schema_ref = 'yaah.execution.completed.v1'
              AND json_extract(body_json, '$.session_id') LIKE 'sub-%'
            GROUP BY 1
            ORDER BY 1
            """
        ).fetchall()
    except sqlite3.Error:
        return [], "none"
    finally:
        con.close()
    return [
        {
            "role": (session or "?").split("-sess-", 1)[0],
            "model": "?",
            "tokens_in": int(prompt or 0),
            "tokens_out": int(completion or 0),
            "dispatches": 1,
        }
        for session, prompt, completion, _ in rows
    ], "final-turn"


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


def _print_task(r, usage, subs, sub_source, show_subs):
    secs = agent_seconds(r)
    line = (
        f"{r['task_id']:24s} resolved={str(r['is_resolved']):5s} "
        f"time={secs if secs is not None else '?':>5}"
    )
    if usage:
        line += (
            f" in={usage['tokens_in']:>9} out={usage['tokens_out']:>7} "
            f"cached={usage['tokens_cached']:>9}"
        )
        if usage["sub_tokens_in"] or usage["sub_tokens_out"]:
            line += (
                f" orch_in={usage['orch_tokens_in']:>8} "
                f"orch_out={usage['orch_tokens_out']:>7} "
                f"sub_in={usage['sub_tokens_in']:>8} "
                f"sub_out={usage['sub_tokens_out']:>7} "
                f"subs={sum(sub['dispatches'] for sub in subs):>2}"
            )
    print(line)
    if show_subs and subs:
        if sub_source == "final-turn":
            print("  (per-sub detail unavailable: older run, final turn only)")
        for sub in subs:
            print(
                f"  {sub['role']:20s} dispatches={sub['dispatches']:>2} "
                f"in={sub['tokens_in']:>8} out={sub['tokens_out']:>7} "
                f"model={sub['model']}"
            )


def main(argv):
    flags = {arg for arg in argv if arg.startswith("--")}
    positional = [arg for arg in argv if not arg.startswith("--")]
    show_subs = "--subs" in flags
    as_json = "--json" in flags
    if len(positional) < 3:
        print(__doc__)
        return 2
    runs_dir, run_ids = positional[1], positional[2:]

    report = {"runs": [], "totals": {}}
    totals = {
        run_id: {
            "tokens_in": 0,
            "tokens_out": 0,
            "tokens_cached": 0,
            "sub_tokens_in": 0,
            "sub_tokens_out": 0,
            "tasks": 0,
        }
        for run_id in run_ids
    }

    for run_id in run_ids:
        with open(f"{runs_dir}/{run_id}/results.json") as f:
            results = json.load(f)["results"]
        run_report = {"run_id": run_id, "tasks": []}
        if not as_json:
            print(f"=== {run_id}")
        for r in sorted(results, key=lambda x: x["task_id"]):
            usage = yaah_usage(runs_dir, run_id, r["task_id"])
            subs, sub_source = sub_session_usage(runs_dir, run_id, r["task_id"])
            if usage is None:
                fallback = usage_report_tokens(runs_dir, run_id, r["task_id"])
                if fallback:
                    tokens_in, tokens_out, cached, n = fallback
                    usage = {
                        "tokens_in": tokens_in,
                        "tokens_out": tokens_out,
                        "tokens_cached": cached,
                        "sub_tokens_in": 0,
                        "sub_tokens_out": 0,
                        "orch_tokens_in": tokens_in,
                        "orch_tokens_out": tokens_out,
                        "sessions": n,
                    }
            if not as_json:
                _print_task(r, usage, subs, sub_source, show_subs)
            run_report["tasks"].append(
                {
                    "task_id": r["task_id"],
                    "resolved": r["is_resolved"],
                    "agent_seconds": agent_seconds(r),
                    "usage": usage,
                    "sub_usage_source": sub_source,
                    "sub_sessions": subs,
                }
            )
            if usage:
                totals[run_id]["tokens_in"] += usage["tokens_in"]
                totals[run_id]["tokens_out"] += usage["tokens_out"]
                totals[run_id]["tokens_cached"] += usage["tokens_cached"]
                totals[run_id]["sub_tokens_in"] += usage["sub_tokens_in"]
                totals[run_id]["sub_tokens_out"] += usage["sub_tokens_out"]
                totals[run_id]["tasks"] += 1
        if not as_json:
            print()
        report["runs"].append(run_report)

    for run_id, t in totals.items():
        if not t["tasks"]:
            continue
        pct = 100 * t["tokens_cached"] / t["tokens_in"] if t["tokens_in"] else 0
        orch_in = t["tokens_in"] - t["sub_tokens_in"]
        orch_out = t["tokens_out"] - t["sub_tokens_out"]
        if not as_json:
            print(
                f"{run_id}: {t['tasks']} tasks with accounting, "
                f"in={t['tokens_in']:,} out={t['tokens_out']:,} "
                f"cached={t['tokens_cached']:,} ({pct:.0f}% of prompt tokens)"
            )
            if t["sub_tokens_in"] or t["sub_tokens_out"]:
                share = (
                    100 * (t["sub_tokens_in"] + t["sub_tokens_out"])
                    / (t["tokens_in"] + t["tokens_out"])
                )
                print(
                    f"  orchestrator in={orch_in:,} out={orch_out:,} | "
                    f"sub-agents in={t['sub_tokens_in']:,} "
                    f"out={t['sub_tokens_out']:,} ({share:.0f}% of total tokens)"
                )
        report["totals"][run_id] = {
            **t,
            "orch_tokens_in": orch_in,
            "orch_tokens_out": orch_out,
            "cached_pct": pct,
        }

    if as_json:
        print(json.dumps(report, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
