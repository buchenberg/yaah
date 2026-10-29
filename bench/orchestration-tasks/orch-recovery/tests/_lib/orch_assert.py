"""Shared three-layer verifier helpers for orchestration benchmark tasks.

The three layers are:

1. end state (task-specific, in ``test_outputs.py``),
2. workspace integrity (seed-anchored git diff + trap byte identity),
3. orchestration trace (sub-sessions, overlap, token share, settlements).

Layer 3 reads the Shepherd trace store and yaah ``state.db`` read-only. When
those artifacts are absent (for example while running ``solution.sh`` for task
validation), the trace helpers call ``pytest.skip`` instead of failing: the
oracle is a deterministic shell script, it never produces a model trace.

This file is the source of truth. ``sync-lib.sh`` vendors it into each task's
``tests/_lib/`` because Terminal-Bench only exposes the task's own tests
directory as ``$TEST_DIR``.
"""

from __future__ import annotations

import os
import sqlite3
import subprocess
from datetime import datetime
from pathlib import Path

import pytest

APP = Path(os.environ.get("ORCH_APP_DIR", "/app"))

TRACE_DIR = Path(
    os.environ.get(
        "ORCH_TRACE_DIR",
        os.environ.get("YAAH_TRACE_DIR", "/agent-logs/yaah-traces"),
    )
)
TRACE_DB = Path(os.environ.get("ORCH_TRACE_DB", TRACE_DIR / "trace.sqlite"))
YAAH_HOME = Path(os.environ.get("YAAH_HOME", "/agent-logs/yaah-home"))
STATE_DB = Path(os.environ.get("ORCH_STATE_DB", YAAH_HOME / "state.db"))
SEED_FILE = Path(os.environ.get("ORCH_SEED_FILE", "/etc/orch-seed"))

_TIMESTAMP_FORMAT = "%Y-%m-%d %H:%M:%S"

# Harness artifacts that legitimately appear in /app while tests run; they are
# not properties of the agent's work and must not trip the integrity layer.
_TEST_ARTIFACTS = {".tbench-testing", ".pytest_cache", "__pycache__", ".yaah"}

# Oracle runs are deterministic shell scripts with no model trace. The test
# wrapper sets ORCH_ORACLE=1; a real agent run cannot set the verifier's
# environment, so missing evidence fails instead of skipping. Do not add a
# filesystem marker: the agent runs as root and could forge it.
_ORACLE = os.environ.get("ORCH_ORACLE") == "1"


def _require_artifact(path: Path, kind: str) -> None:
    if path.is_file():
        return
    if _ORACLE:
        pytest.skip(f"{kind} not found: {path} (oracle run)")
    pytest.fail(
        f"missing {kind}: {path}; a real agent run must produce it "
        "(set ORCH_ORACLE=1 only for a deterministic oracle run)"
    )


def seed() -> str:
    """Return the fixture seed commit captured when the image was built."""
    from_env = os.environ.get("ORCH_SEED", "").strip()
    if from_env:
        return from_env
    if SEED_FILE.is_file():
        value = SEED_FILE.read_text().strip()
        if value:
            return value
    roots = _git("rev-list", "--max-parents=0", "HEAD").split()
    if not roots:
        pytest.fail("cannot determine the fixture seed commit")
    return roots[-1]


def _git(*args: str, text: bool = True):
    proc = subprocess.run(
        ["git", "-C", str(APP), *args],
        capture_output=True,
        check=False,
    )
    if proc.returncode != 0:
        pytest.fail(
            f"git {' '.join(args)} failed: {proc.stderr.decode(errors='replace').strip()}"
        )
    return proc.stdout.decode() if text else proc.stdout


def _connect(path: Path) -> sqlite3.Connection:
    kind = "trace database" if path == TRACE_DB else "state database"
    _require_artifact(path, kind)
    conn = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
    return conn


def _parse_ts(value: str) -> datetime:
    return datetime.strptime(value, _TIMESTAMP_FORMAT)


def _sub_session_windows() -> list[dict]:
    """Return one window per sub-agent session seen in the trace store.

    A sub-session is identified by the ``session_id`` on its
    ``yaah.execution.started.v1`` facts.  Its window runs from the earliest
    start fact to the latest completed/failed fact (or its last start fact
    when the run has no terminal fact yet).
    """
    conn = _connect(TRACE_DB)
    try:
        starts = conn.execute(
            """
            SELECT json_extract(r.body_json, '$.session_id'),
                   MIN(ai.committed_at), MAX(ai.committed_at)
            FROM records r
            JOIN append_intents ai ON ai.append_intent_id = r.append_intent_id
            WHERE r.schema_ref = 'yaah.execution.started.v1'
            GROUP BY 1
            """
        ).fetchall()
        ends = conn.execute(
            """
            SELECT json_extract(r.body_json, '$.session_id'), MAX(ai.committed_at)
            FROM records r
            JOIN append_intents ai ON ai.append_intent_id = r.append_intent_id
            WHERE r.schema_ref IN ('yaah.execution.completed.v1',
                                   'yaah.execution.failed.v1')
            GROUP BY 1
            """
        ).fetchall()
    finally:
        conn.close()

    end_by_session = {session: ts for session, ts in ends if session}
    windows = []
    for session, first_start, last_start in starts:
        if not session or not session.startswith("sub-"):
            continue
        start = _parse_ts(first_start)
        # Unfinished sessions fall back to their LAST start fact: using the
        # first start collapses the window to zero width and under-counts
        # overlaps.
        end = _parse_ts(end_by_session.get(session, last_start))
        windows.append({"session": session, "start": start, "end": end})
    windows.sort(key=lambda w: (w["start"], w["end"]))
    return windows


def assert_delegate_count(minimum: int = 1) -> None:
    """At least ``minimum`` distinct sub-agent sessions ran."""
    windows = _sub_session_windows()
    assert len(windows) >= minimum, (
        f"expected at least {minimum} delegated sub-agent sessions, "
        f"found {len(windows)}; the orchestrator did the work inline"
    )


def assert_parallel_dispatch(minimum: int = 1) -> None:
    """At least ``minimum`` sub-agent sessions overlapped in wall-clock time."""
    windows = _sub_session_windows()
    events = []
    for window in windows:
        events.append((window["start"], 1))
        events.append((window["end"], -1))
    # Ends sort before starts at the same instant so back-to-back runs do not
    # count as overlapping.
    events.sort(key=lambda event: (event[0], event[1]))

    concurrent = peak = 0
    for _, delta in events:
        concurrent += delta
        peak = max(peak, concurrent)
    assert peak >= minimum, (
        f"expected at least {minimum} concurrent sub-agent sessions, peak was "
        f"{peak}; delegation was serialized or inline"
    )


def assert_sub_token_share() -> None:
    """The session recorded non-zero sub-agent token usage."""
    conn = _connect(STATE_DB)
    try:
        row = conn.execute(
            """
            SELECT sub_tokens_in, sub_tokens_out
            FROM sessions
            ORDER BY started_at DESC
            LIMIT 1
            """
        ).fetchone()
    finally:
        conn.close()
    if row is None:
        pytest.skip("no session row in state.db")
    sub_in, sub_out = (row[0] or 0), (row[1] or 0)
    assert sub_in + sub_out > 0, (
        "no sub-agent tokens were recorded; the orchestration trace is empty"
    )


def settled_actions() -> set[str]:
    """Return the settlement actions recorded in the trace store."""
    conn = _connect(TRACE_DB)
    try:
        rows = conn.execute(
            """
            SELECT DISTINCT json_extract(r.body_json, '$.action')
            FROM records r
            WHERE r.schema_ref = 'shepherd.run_output.settled.v1'
            """
        ).fetchall()
    finally:
        conn.close()
    return {action for (action,) in rows if action}


def settled_output_ids() -> set[str]:
    """Return the settled output IDs recorded in the trace store."""
    conn = _connect(TRACE_DB)
    try:
        rows = conn.execute(
            "SELECT DISTINCT json_extract(body_json, '$.output_id') FROM records "
            "WHERE schema_ref = 'shepherd.run_output.settled.v1'"
        ).fetchall()
    finally:
        conn.close()
    return {output for (output,) in rows if output}


def assert_settle_verbs(expected: set[str]) -> None:
    """Every verb in ``expected`` appears in the settlement record."""
    actions = settled_actions()
    missing = set(expected) - actions
    assert not missing, (
        f"settlement record is missing {sorted(missing)}; recorded actions: "
        f"{sorted(actions)}"
    )


def sealed_output_count() -> int:
    """Count sealed retained outputs recorded in the trace store."""
    conn = _connect(TRACE_DB)
    try:
        rows = conn.execute(
            "SELECT COUNT(DISTINCT json_extract(body_json, '$.output_id')) FROM records "
            "WHERE schema_ref = 'shepherd.run_output.sealed.v1'"
        ).fetchall()
    finally:
        conn.close()
    return rows[0][0] if rows else 0


def assert_any_settled_output() -> None:
    """At least one settlement recorded an output id.

    Positive exercise for the settled-output schema family: proposal-lane
    tasks pair this with assert_settle_verbs so a schema-name drift fails
    loudly instead of silently satisfying assert_no_proposals' zero-count.
    """
    ids = settled_output_ids()
    assert ids, (
        "no settled output ids recorded; the settlement records are missing "
        "their output_id payload"
    )


def assert_no_proposals() -> None:
    """The run produced no sealed outputs and settled nothing (yaah-7yf).

    A report-lane fan-out must leave the proposal surface untouched: no
    sealed worktree deltas, no settlement records. Worktree leftovers are
    asserted separately (assert_no_orphan_worktrees).
    """
    assert settled_actions() == set(), (
        f"report-lane dispatches must not settle proposals; recorded: "
        f"{sorted(settled_actions())}"
    )
    sealed = sealed_output_count()
    assert sealed == 0, (
        f"report-lane dispatches must not seal proposals; found {sealed} "
        "sealed output(s) in the trace"
    )


def assert_no_orphan_worktrees() -> None:
    """No retained-run worktrees were left behind.

    The default worktree root is ``.yaah/shepherd-worktrees`` inside the
    repository path; ``ORCH_WORKTREE_ROOT``
    overrides the search when a scenario declares a different root.
    """
    candidates = []
    override = os.environ.get("ORCH_WORKTREE_ROOT")
    if override:
        candidates.append(Path(override))
    candidates.extend([APP / ".yaah" / "shepherd-worktrees"])

    leftovers = []
    for root in candidates:
        if not root.is_dir():
            continue
        leftovers.extend(sorted(p.name for p in root.iterdir()))
    assert not leftovers, (
        "orphan retained worktrees left behind: " + ", ".join(leftovers)
    )


def assert_no_unexpected_changes(seed_commit: str, allowlist: list[str]) -> None:
    """Only allowlisted paths differ from the seed commit.

    Covers tracked changes (committed or not) and untracked files, the same
    discipline as the historical shepherd benchmark fixture verifier.
    """
    changed = set(_git("diff", "--name-only", seed_commit).splitlines())
    changed.update(_git("ls-files", "--others", "--exclude-standard").splitlines())
    unexpected = sorted(
        path
        for path in changed
        if path
        and path.split("/", 1)[0] not in _TEST_ARTIFACTS
        and not _allowed(path, allowlist)
    )
    assert not unexpected, (
        "workspace changed outside the allowlist: " + ", ".join(unexpected)
    )


def assert_byte_identical(seed_commit: str, relpath: str) -> None:
    """The file at ``relpath`` is byte-identical to its seeded content."""
    expected = _git("show", f"{seed_commit}:{relpath}", text=False)
    target = APP / relpath
    assert target.is_file(), f"expected fixture file is missing: {target}"
    actual = target.read_bytes()
    assert actual == expected, f"{relpath} was modified (byte identity lost)"


def _allowed(path: str, allowlist: list[str]) -> bool:
    for entry in allowlist:
        entry = entry.rstrip("/")
        if path == entry or path.startswith(entry + "/"):
            return True
    return False


def assert_subagent_role(role: str) -> None:
    """At least one sub-session ran under ``role``."""
    windows = _sub_session_windows()
    prefix = f"sub-{role}-"
    matches = [w for w in windows if w["session"].startswith(prefix)]
    assert matches, (
        f"no sub-agent session for role {role!r}; saw "
        f"{[w['session'] for w in windows] or 'none'}"
    )


def assert_tool_failures(minimum: int = 1) -> None:
    """At least ``minimum`` sub-agent tool captures failed."""
    conn = _connect(TRACE_DB)
    try:
        row = conn.execute(
            """
            SELECT COUNT(DISTINCT r.record_id)
            FROM records r
            JOIN path_entries pe ON pe.record_id = r.record_id
            WHERE r.schema_ref LIKE 'yaah.tool.%.applied'
              AND json_extract(r.body_json, '$.success') = 0
              AND pe.path_ref LIKE 'sub-%'
            """
        ).fetchone()
    finally:
        conn.close()
    failures = row[0] if row else 0
    assert failures >= minimum, (
        f"expected at least {minimum} failed tool captures, found {failures}"
    )


def assert_confinement_recorded() -> None:
    """The sub-agent dispatch recorded an *enforced* confinement placement.

    A fact exists even when ``auto`` degrades to an advisory host path, so
    existence alone is not evidence. The scenario requests ``paddock`` (fail
    closed) and this check requires ``enforced: true`` with a non-advisory
    resolution.
    """
    conn = _connect(TRACE_DB)
    try:
        row = conn.execute(
            """
            SELECT COUNT(*) FROM records
            WHERE schema_ref = 'yaah.subagent.confinement.v1'
              AND json_extract(body_json, '$.enforced') = 1
              AND json_extract(body_json, '$.resolved') != 'advisory'
            """
        ).fetchone()
    finally:
        conn.close()
    assert row and row[0] > 0, (
        "no enforced yaah.subagent.confinement.v1 fact was recorded; "
        "the dispatch degraded to advisory"
    )


def session_messages() -> list[dict]:
    """Return the persisted messages of the most recent session."""
    conn = _connect(STATE_DB)
    try:
        session = conn.execute(
            "SELECT id FROM sessions ORDER BY started_at DESC LIMIT 1"
        ).fetchone()
        if session is None:
            pytest.skip("no session row in state.db")
        rows = conn.execute(
            """
            SELECT idx, role, tool_name, content, tool_call_id, tool_calls
            FROM messages
            WHERE session_id = ?
            ORDER BY idx
            """,
            (session[0],),
        ).fetchall()
    finally:
        conn.close()
    return [
        {
            "idx": idx,
            "role": role,
            "tool_name": tool_name,
            "content": content,
            "tool_call_id": tool_call_id,
            "tool_calls": tool_calls,
        }
        for idx, role, tool_name, content, tool_call_id, tool_calls in rows
    ]


def assert_message_contains(text: str) -> None:
    """Some persisted message contains ``text``."""
    messages = session_messages()
    assert any(text in (m["content"] or "") for m in messages), (
        f"no persisted message contains {text!r}"
    )


def assert_tool_message_contains(text: str) -> None:
    """A tool result contains ``text``.

    User and assistant text can echo the instruction, so orchestration
    evidence must come from an actual tool result.
    """
    messages = session_messages()
    assert any(
        m["role"] == "tool" and text in (m["content"] or "") for m in messages
    ), f"no tool result contains {text!r}"


def assert_tool_calls(name: str, minimum: int = 1) -> None:
    """At least ``minimum`` assistant tool calls used ``name``."""
    import json

    count = 0
    for message in session_messages():
        if message["role"] != "assistant" or not message["tool_calls"]:
            continue
        for call in json.loads(message["tool_calls"]):
            if (call.get("function") or {}).get("name") == name:
                count += 1
    assert count >= minimum, (
        f"expected at least {minimum} {name!r} tool calls, found {count}"
    )


def assert_session_summary() -> None:
    """The latest session persisted a compaction summary.

    The conversation length alone does not prove a rollover (a batched
    read loop is short too). ``sessions.compacted_summary`` is written only when
    compaction produces a summary.
    """
    conn = _connect(STATE_DB)
    try:
        row = conn.execute(
            "SELECT compacted_summary FROM sessions ORDER BY started_at DESC LIMIT 1"
        ).fetchone()
    finally:
        conn.close()
    assert row is not None, "no session row in state.db"
    assert (row[0] or "").strip(), "no compaction summary was persisted"


def assert_tool_call_linkage() -> None:
    """Every assistant tool call has a matching tool result after it.

    Compaction must preserve the call/result pairs it keeps; an orphaned call
    makes the next provider request invalid.
    """
    import json

    messages = session_messages()
    pending = set()
    for message in messages:
        if message["role"] == "assistant" and message["tool_calls"]:
            for call in json.loads(message["tool_calls"]):
                pending.add(call.get("id", ""))
        elif message["role"] == "tool":
            pending.discard(message["tool_call_id"] or "")
    assert not pending, f"orphaned tool calls after compaction: {sorted(pending)}"


def assert_message_count_below(maximum: int) -> None:
    """The persisted conversation stayed below ``maximum`` messages.

    A forced context rollover replaces summarized turns, so the rebased
    conversation must be shorter than an uncompacted run's full transcript.
    """
    count = len(session_messages())
    assert count < maximum, (
        f"conversation has {count} persisted messages (limit {maximum}); "
        "compaction did not roll the context over"
    )



def worktree_registered(path: Path) -> bool:
    """Whether path is registered as a git worktree of /app."""
    try:
        out = _git("worktree", "list", "--porcelain")
    except Exception:
        return False
    target = str(path.resolve())
    for line in out.splitlines():
        if line.startswith("worktree ") and str(Path(line[len("worktree "):]).resolve()) == target:
            return True
    return False


def recovered_scope_exists(owner: str, path: Path) -> bool:
    """Whether the trace carries a fork record for owner at path."""
    conn = _connect(TRACE_DB)
    try:
        rows = conn.execute(
            """
            SELECT json_extract(body_json, '$.isolated'),
                   json_extract(body_json, '$.child_owner'),
                   json_extract(body_json, '$.sandbox_path')
            FROM records
            WHERE schema_ref = 'goatherd.scope.forked.v1'
            """
        ).fetchall()
    finally:
        conn.close()
    target = str(path)
    for isolated, child_owner, sandbox_path in rows:
        if (
            str(isolated).lower() in ("1", "true")
            and child_owner == owner
            and sandbox_path == target
        ):
            return True
    return False


def sealed_output_count() -> int:
    conn = _connect(TRACE_DB)
    try:
        return conn.execute(
            "SELECT COUNT(*) FROM records WHERE schema_ref = 'goatherd.run_output.sealed.v1'"
        ).fetchone()[0]
    finally:
        conn.close()
