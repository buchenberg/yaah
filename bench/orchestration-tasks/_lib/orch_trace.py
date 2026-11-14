"""Spec-driven orchestration assertions (bead yaah-0u0v.18).

The hand-driven bench verifies a run from three layers (see ``orch_assert``):
end state, workspace integrity, and the orchestration trace. A spec-driven run
produces the same three, but its trace is not the Shepherd store — it is the
document ``yaah workflow explain --from-trace <run_id> --json`` prints (kind
``yaah.workflow.trace.v1``, see ``internal/workflow/trace``). This module is
that document's reader.

It is deliberately a SIBLING of ``orch_assert``, not a fork: the layers that
read the workspace are re-used from there (``APP``, ``seed``,
``assert_no_unexpected_changes``, ``assert_byte_identical``), and only the
trace layer is new, because the trace store differs. Nothing here re-derives a
node's outcome — every fact is quoted from the record the run itself wrote.

Usage (see bench/orchestration-tasks/spec-run.sh):

    python3 _lib/orch_trace.py --trace trace.json --task orch-fanout
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

try:  # imported as `_lib.orch_trace` from a task's tests/, or as a script
    from _lib import orch_assert as oa
except ImportError:  # pragma: no cover - direct script use
    from pathlib import Path as _P

    sys.path.insert(0, str(_P(__file__).resolve().parent))
    import orch_assert as oa

TRACE_KIND = "yaah.workflow.trace.v1"

# The per-task facts a spec-driven run must show. Values are the minimums the
# task's own verifier asserts for the hand-driven form (orch-fanout:
# `assert_delegate_count(3)`, `assert_parallel_dispatch(3)`), so a spec run is
# held to the same bar as a hand run.
TASKS = {
    "orch-fanout": {"delegates": 6, "peak_parallel": 3, "artifacts": 6},
}


def load(path: str | Path) -> dict:
    """Read one trace report, refusing a document of another kind."""
    report = json.loads(Path(path).read_text())
    if report.get("kind") != TRACE_KIND:
        raise SystemExit(
            f"{path}: kind is {report.get('kind')!r}, want {TRACE_KIND!r}; "
            "this reader consumes `yaah workflow explain --from-trace --json`"
        )
    return report


def peak_parallel(records: list[dict]) -> int:
    """Highest number of dispatch attempts open at once.

    A ``dispatch`` record with status ``started`` is an attempt handed to the
    door; any other status on a dispatch record is that attempt's envelope
    coming back, so the pair closes. This is the record-side stand-in for
    ``orch_assert.assert_parallel_dispatch``: the hand-driven form measures
    wall-clock overlap in the trace store, the spec-driven form counts open
    attempts in the run's own sequence, and the declared
    ``max_concurrent_dispatches`` is what bounds it.
    """
    in_flight = peak = 0
    for record in records:
        if record.get("kind") != "dispatch":
            continue
        if record.get("status") == "started":
            in_flight += 1
            peak = max(peak, in_flight)
        else:
            in_flight -= 1
    return peak


def dispatched_steps(report: dict) -> dict[str, str]:
    """Step id -> node path for every delegate the run actually dispatched."""
    out: dict[str, str] = {}
    for record in report.get("records", []):
        if record.get("kind") != "dispatch" or record.get("status") != "started":
            continue
        out[record.get("node_id", record.get("node_path", ""))] = record.get(
            "node_path", ""
        )
    return out


def assert_trace(task: str, report: dict) -> None:
    """Assert one task's orchestration facts over the run's own trace."""
    expect = TASKS[task]
    assert report.get("status") == "succeeded", (
        f"{task}: run finished {report.get('status')!r} — {report.get('reason', '')}"
    )
    # A run that diverged from the document is a finding even when it
    # succeeded: the document is supposed to be the dispatcher.
    assert not report.get("divergences"), (
        f"{task}: the run diverged from its spec: {report['divergences']}"
    )
    steps = dispatched_steps(report)
    assert len(steps) >= expect["delegates"], (
        f"{task}: {len(steps)} dispatch(es) recorded, want at least "
        f"{expect['delegates']}; the orchestrator did the work inline"
    )
    peak = peak_parallel(report.get("records", []))
    assert peak >= expect["peak_parallel"], (
        f"{task}: peak concurrent dispatches was {peak}, want at least "
        f"{expect['peak_parallel']}; delegation was serialized"
    )
    states = {node["path"]: node["state"] for node in report.get("nodes", [])}
    not_succeeded = sorted(p for p, s in states.items() if s != "succeeded")
    assert not not_succeeded, f"{task}: nodes did not succeed: {not_succeeded}"
    # A report-lane bench seals nothing and settles nothing.
    assert not report.get("proposals"), (
        f"{task}: report-lane dispatches must not seal proposals; "
        f"recorded {report['proposals']}"
    )
    assert not report.get("settlements"), (
        f"{task}: nothing should be settled; recorded {report['settlements']}"
    )


def assert_end_state(task: str, artifact_root: str | Path, report: dict) -> None:
    """The run's recorded artifacts are the task's end state, on disk."""
    expect = TASKS[task]["artifacts"]
    node_artifacts = [
        node["path"]
        for node in report.get("nodes", [])
        if node.get("kind") == "emit" and node.get("outputs")
    ]
    assert len(node_artifacts) == expect, (
        f"{task}: {len(node_artifacts)} emit node(s) recorded an output, want {expect}"
    )
    root = Path(artifact_root)
    audits = root / "audits"
    assert audits.is_dir(), f"{task}: no audits/ directory under {root}"
    files = sorted(path.name for path in audits.iterdir() if path.is_file())
    assert len(files) == expect, f"{task}: audits/ holds {files}, want {expect} file(s)"


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--trace", required=True, help="trace.v1 JSON report")
    parser.add_argument("--task", default="orch-fanout", choices=sorted(TASKS))
    parser.add_argument(
        "--artifact-root",
        default=str(oa.APP),
        help="directory the run's emit artifacts were written under",
    )
    args = parser.parse_args(argv)
    report = load(args.trace)
    assert_trace(args.task, report)
    assert_end_state(args.task, args.artifact_root, report)
    print(
        f"{args.task}: trace ok — {len(dispatched_steps(report))} dispatch(es), "
        f"peak {peak_parallel(report['records'])} concurrent, "
        f"0 divergences"
    )
    return 0


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
