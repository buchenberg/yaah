#!/usr/bin/env python3
"""Extract per-task metrics from Terminal-Bench run directories.

Prints verdict, agent wall time, normalized token accounting, steps, and
cost for each task in a run — the same numbers recorded in RESULTS.md.
Accounting sources: yaah sums its in-container state.db (sessions table,
tokens_in includes cached); hermes and aider read their usage-report JSONs,
whose input_tokens exclude (hermes) or include (aider) cached buckets —
normalized to the common schema via each report's prompt_includes_cache
flag. opencode sums the JSON `step_finish` events from its adapter, with the
session database as a fallback.

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
import os
import sqlite3
import sys
from pathlib import Path

METRICS_SCHEMA = "terminal-bench.metrics.v1"
DEFAULT_PRICING = Path(__file__).resolve().parent / "pricing.json"
DEFAULT_MODEL = os.environ.get("TB_MODEL", "deepseek-flash")


def load_pricing():
    """Load the shared per-million-token pricing table. Return None if absent."""
    path = Path(os.environ.get("TB_PRICING", DEFAULT_PRICING))
    try:
        with path.open() as f:
            data = json.load(f)
    except (OSError, ValueError):
        return None
    if not isinstance(data, dict) or not isinstance(data.get("models"), dict):
        return None
    data["_path"] = str(path)
    return data


def pricing_key(model, pricing):
    if not model or not pricing:
        return None
    models = pricing.get("models", {})
    if model in models:
        return model
    # Agent adapters often spell the provider prefix (`deepseek/deepseek-flash`)
    # while the price sheet keys the model alone.
    tail = model.rsplit("/", 1)[-1]
    if tail in models:
        return tail
    return None


def _nonneg_int(value):
    try:
        return max(int(value or 0), 0)
    except (TypeError, ValueError, OverflowError):
        return 0


def token_record(input_uncached, output, cache_read=0, cache_write=0, reasoning=None):
    return {
        "input": _nonneg_int(input_uncached),
        "output": _nonneg_int(output),
        "reasoning": None if reasoning is None else _nonneg_int(reasoning),
        "cache_read": _nonneg_int(cache_read),
        "cache_write": _nonneg_int(cache_write),
    }


def pricing_period(metadata):
    """Return the DeepSeek peak/off-peak tier for a run.

    Peak hours are 01:00-04:00 and 06:00-10:00 UTC, Monday-Friday. If the
    metadata has no parseable start time, assume off-peak and record that the
    tier was assumed rather than silently charging peak rates.
    """
    raw = (metadata or {}).get("start_time")
    if not raw:
        return "off_peak", "assumed"
    try:
        started = datetime.datetime.fromisoformat(raw)
    except (TypeError, ValueError):
        return "off_peak", "assumed"
    if started.tzinfo is None:
        started = started.replace(tzinfo=datetime.timezone.utc)
        source = "metadata-naive-utc"
    else:
        started = started.astimezone(datetime.timezone.utc)
        source = "metadata"
    if started.weekday() < 5 and (1 <= started.hour < 4 or 6 <= started.hour < 10):
        return "peak", source
    return "off_peak", source


def compute_cost(tokens, model, pricing, period="off_peak"):
    if not tokens or not pricing:
        return None, None
    key = pricing_key(model, pricing)
    rates = pricing.get("models", {}).get(key) if key else None
    if not isinstance(rates, dict):
        return None, key
    if isinstance(rates.get(period), dict):
        rates = rates[period]
    total = 0.0
    try:
        for field in ("input", "cache_read", "cache_write"):
            total += float(rates.get(field, 0) or 0) * float(tokens.get(field, 0) or 0)
        # Reasoning tokens are billed at the output rate.
        billable_output = float(tokens.get("output", 0) or 0) + float(tokens.get("reasoning", 0) or 0)
        total += float(rates.get("output", 0) or 0) * billable_output
    except (TypeError, ValueError):
        return None, key
    return total / 1_000_000.0, key


def run_metadata(runs_dir, run_id):
    path = f"{runs_dir}/{run_id}/run_metadata.json"
    try:
        with open(path) as f:
            data = json.load(f)
    except (OSError, ValueError):
        return {}
    return data if isinstance(data, dict) else {}


def run_arm(metadata):
    return metadata.get("agent_name") or "unknown"


def run_model(metadata):
    model = metadata.get("model_name")
    if model:
        return model
    kwargs = metadata.get("agent_kwargs") or {}
    if isinstance(kwargs, dict):
        return kwargs.get("model")
    return None


def run_effort(metadata):
    kwargs = metadata.get("agent_kwargs") or {}
    if isinstance(kwargs, dict):
        return kwargs.get("effort") or kwargs.get("reasoning_effort")
    return None


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
        columns = {row[1].lower() for row in con.execute("PRAGMA table_info(sessions)")}
        def total(column):
            return f"COALESCE(SUM({column}),0)" if column.lower() in columns else "0"
        row = con.execute(
            f"SELECT {total('tokens_in')}, {total('tokens_out')}, "
            f"{total('tokens_cached')}, {total('sub_tokens_in')}, "
            f"{total('sub_tokens_out')}, COUNT(*) FROM sessions"
        ).fetchone()
        try:
            steps = con.execute(
                "SELECT COUNT(*) FROM messages WHERE role = 'assistant'"
            ).fetchone()[0]
        except sqlite3.OperationalError:
            steps = None
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
        "steps": steps,
        "tokens": token_record(
            max(tokens_in - cached, 0), tokens_out, cached, 0
        ),
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


def opencode_usage(runs_dir, run_id, task_id):
    """Read the OpenCode arm's JSON event stream, then the session DB fallback.

    The stream is authoritative for the root session's steps and token
    buckets. Child sessions live in `session_v2` with a parent_id; their
    tokens are reported separately so the same sub-agent rule applies to every
    arm.
    """
    stream_paths = glob.glob(
        f"{runs_dir}/{run_id}/{task_id}/*/agent-logs/opencode-stdout.jsonl"
    )
    steps = 0
    input_tokens = output_tokens = reasoning_tokens = 0
    cache_read = cache_write = 0
    reported_cost = 0.0
    root_session = ""
    if stream_paths:
        with open(stream_paths[0]) as f:
            for line in f:
                try:
                    event = json.loads(line)
                except ValueError:
                    continue
                part = event.get("part") or {}
                root_session = root_session or event.get("sessionID") or ""
                if event.get("type") != "step_finish":
                    continue
                steps += 1
                tokens = part.get("tokens") or {}
                input_tokens += _nonneg_int(tokens.get("input"))
                output_tokens += _nonneg_int(tokens.get("output"))
                reasoning_tokens += _nonneg_int(tokens.get("reasoning"))
                cache = tokens.get("cache") or {}
                cache_read += _nonneg_int(cache.get("read"))
                cache_write += _nonneg_int(cache.get("write"))
                try:
                    reported_cost += float(part.get("cost") or 0)
                except (TypeError, ValueError):
                    pass

    session_file = glob.glob(
        f"{runs_dir}/{run_id}/{task_id}/*/agent-logs/opencode-session.txt"
    )
    if session_file:
        try:
            root_session = Path(session_file[0]).read_text().strip() or root_session
        except OSError:
            pass

    sub_in = sub_out = 0
    sub_sessions = 0
    db_paths = glob.glob(
        f"{runs_dir}/{run_id}/{task_id}/*/agent-logs/opencode-data/opencode/opencode.db"
    )
    if db_paths and root_session:
        try:
            con = sqlite3.connect(f"file:{db_paths[0]}?mode=ro", uri=True)
            rows = con.execute(
                "SELECT id, parent_id, tokens_input, tokens_output, "
                "tokens_reasoning, tokens_cache_read, tokens_cache_write FROM session_v2"
            ).fetchall()
            con.close()
            by_parent = {}
            for sid, parent, tin, tout, treason, tread, twrite in rows:
                by_parent.setdefault(parent, []).append(
                    (sid, tin, tout, treason, tread, twrite)
                )
            stack = list(by_parent.get(root_session, []))
            while stack:
                sid, tin, tout, treason, tread, twrite = stack.pop()
                sub_sessions += 1
                child_in = _nonneg_int(tin)
                child_out = _nonneg_int(tout)
                child_reason = _nonneg_int(treason)
                child_read = _nonneg_int(tread)
                child_write = _nonneg_int(twrite)
                # session_v2 input is uncached. Fold children into the total
                # token buckets; the sub_* counters keep the split explicit.
                input_tokens += child_in
                output_tokens += child_out
                reasoning_tokens += child_reason
                cache_read += child_read
                cache_write += child_write
                sub_in += child_in + child_read + child_write
                sub_out += child_out
                stack.extend(by_parent.get(sid, []))
        except sqlite3.Error:
            sub_in = sub_out = sub_sessions = 0

    if steps == 0:
        # Session DB fallback when the event stream was lost or empty.
        if not db_paths or not root_session:
            return None
        try:
            con = sqlite3.connect(f"file:{db_paths[0]}?mode=ro", uri=True)
            row = con.execute(
                "SELECT tokens_input, tokens_output, tokens_reasoning, "
                "tokens_cache_read, tokens_cache_write, cost FROM session_v2 WHERE id = ?",
                (root_session,),
            ).fetchone()
            con.close()
        except sqlite3.Error:
            return None
        if not row:
            return None
        input_tokens = _nonneg_int(row[0])
        output_tokens = _nonneg_int(row[1])
        reasoning_tokens = _nonneg_int(row[2])
        cache_read = _nonneg_int(row[3])
        cache_write = _nonneg_int(row[4])
        reported_cost = float(row[5] or 0)
        steps = None

    return {
        "tokens_in": input_tokens + cache_read + cache_write,
        "tokens_out": output_tokens,
        "tokens_cached": cache_read,
        "sub_tokens_in": sub_in,
        "sub_tokens_out": sub_out,
        "orch_tokens_in": input_tokens + cache_read + cache_write,
        "orch_tokens_out": output_tokens,
        "sessions": 1 + sub_sessions,
        "steps": steps,
        "reported_cost_usd": reported_cost,
        "tokens": token_record(
            input_tokens, output_tokens, cache_read, cache_write, reasoning_tokens
        ),
    }


def usage_report_tokens(runs_dir, run_id, task_id):
    """Read a driver usage report (hermes-usage.json or aider-usage.json)
    and normalize to the common token columns plus legacy totals.

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
        raw_input = report.get("input_tokens") or 0
        output = report.get("output_tokens") or 0
        cache_read = report.get("cache_read_tokens") or 0
        cache_write = report.get("cache_write_tokens") or 0
        includes_cache = bool(report.get("prompt_includes_cache"))
        # The legacy "tokens_in" is prompt-includes-cache for every arm.
        prompt = raw_input if includes_cache else raw_input + cache_read + cache_write
        # `prompt_includes_cache` describes cache reads; cache writes are
        # separate and must not be subtracted from uncached input.
        input_uncached = max(raw_input - cache_read, 0) if includes_cache else raw_input
        steps = report.get("api_calls")
        if steps is None:
            steps = report.get("calls")
        reasoning = report.get("reasoning_tokens")
        return {
            "tokens_in": prompt,
            "tokens_out": output,
            "tokens_cached": cache_read,
            "sub_tokens_in": 0,
            "sub_tokens_out": 0,
            "orch_tokens_in": prompt,
            "orch_tokens_out": output,
            "sessions": 1,
            "steps": steps,
            "tokens": token_record(
                input_uncached, output, cache_read, cache_write, reasoning
            ),
        }
    return None


def _print_task(r, usage, subs, sub_source, show_subs, cost=None, steps=None):
    secs = agent_seconds(r)
    line = (
        f"{r['task_id']:24s} resolved={str(r['is_resolved']):5s} "
        f"time={secs if secs is not None else '?':>5}"
    )
    if steps is not None:
        line += f" steps={steps}"
    if cost is not None:
        line += f" cost=${cost:.4f}"
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
    pricing = load_pricing()

    report = {
        "schema": METRICS_SCHEMA,
        "pricing": {
            "currency": (pricing or {}).get("currency", "USD"),
            "unit": (pricing or {}).get("unit", "per_million_tokens"),
            "source": (pricing or {}).get("source"),
        },
        "runs": [],
        "totals": {},
    }
    totals = {
        run_id: {
            "tokens_in": 0,
            "tokens_out": 0,
            "tokens_cached": 0,
            "sub_tokens_in": 0,
            "sub_tokens_out": 0,
            "subagent_tokens": 0,
            "tasks": 0,
            "cost_usd": 0.0,
            "cost_tasks": 0,
            "tokens": token_record(0, 0),
        }
        for run_id in run_ids
    }

    for run_id in run_ids:
        with open(f"{runs_dir}/{run_id}/results.json") as f:
            results = json.load(f)["results"]
        metadata = run_metadata(runs_dir, run_id)
        arm = run_arm(metadata)
        model = run_model(metadata)
        model_source = "metadata" if model else "default"
        if not model:
            model = DEFAULT_MODEL
        effort = run_effort(metadata) or os.environ.get("TB_EFFORT")
        period, period_source = pricing_period(metadata)
        run_report = {
            "run_id": run_id,
            "arm": arm,
            "model": model,
            "model_source": model_source,
            "effort": effort,
            "pricing_period": period,
            "pricing_period_source": period_source,
            "tasks": [],
        }
        if not as_json:
            print(f"=== {run_id} ({arm}{' model=' + model if model else ''})")
        for r in sorted(results, key=lambda x: x["task_id"]):
            usage = yaah_usage(runs_dir, run_id, r["task_id"])
            subs, sub_source = sub_session_usage(runs_dir, run_id, r["task_id"])
            if usage is None:
                usage = opencode_usage(runs_dir, run_id, r["task_id"])
            if usage is None:
                usage = usage_report_tokens(runs_dir, run_id, r["task_id"])
            tokens = usage.get("tokens") if usage else None
            cost, price_key = compute_cost(tokens, model, pricing, period)
            steps = usage.get("steps") if usage else None
            subagent_tokens = 0
            if usage:
                subagent_tokens = max(
                    int(usage.get("sub_tokens_in", 0) or 0)
                    + int(usage.get("sub_tokens_out", 0) or 0),
                    0,
                )
            if not as_json:
                _print_task(r, usage, subs, sub_source, show_subs, cost, steps)
            run_report["tasks"].append(
                {
                    "task_id": r["task_id"],
                    "resolved": r["is_resolved"],
                    "agent_seconds": agent_seconds(r),
                    "wall_time_s": agent_seconds(r),
                    "steps": steps,
                    "tokens": tokens,
                    "cost_usd": cost,
                    "pricing_model": price_key,
                    "pricing_period": period,
                    "pricing_period_source": period_source,
                    "subagent_tokens": subagent_tokens,
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
                totals[run_id]["subagent_tokens"] += subagent_tokens
                totals[run_id]["tasks"] += 1
                if tokens:
                    for field in ("input", "output", "cache_read", "cache_write"):
                        totals[run_id]["tokens"][field] += tokens.get(field, 0) or 0
                    if tokens.get("reasoning") is not None:
                        totals[run_id]["tokens"]["reasoning"] = (
                            (totals[run_id]["tokens"]["reasoning"] or 0)
                            + tokens["reasoning"]
                        )
                if cost is not None:
                    totals[run_id]["cost_usd"] += cost
                    totals[run_id]["cost_tasks"] += 1
        if not as_json:
            print()
        report["runs"].append(run_report)

    for run_id, t in totals.items():
        if not t["tasks"]:
            continue
        pct = 100 * t["tokens_cached"] / t["tokens_in"] if t["tokens_in"] else 0
        orch_in = t["tokens_in"] - t["sub_tokens_in"]
        orch_out = t["tokens_out"] - t["sub_tokens_out"]
        cost = t["cost_usd"] if t["cost_tasks"] else None
        if not as_json:
            print(
                f"{run_id}: {t['tasks']} tasks with accounting, "
                f"in={t['tokens_in']:,} out={t['tokens_out']:,} "
                f"cached={t['tokens_cached']:,} ({pct:.0f}% of prompt tokens)"
                + (f" cost=${cost:.4f}" if cost is not None else "")
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
            "cost_usd": cost,
        }
        # Remove the internal accumulator; it is not part of the schema.
        report["totals"][run_id].pop("cost_tasks", None)

    if as_json:
        print(json.dumps(report, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
