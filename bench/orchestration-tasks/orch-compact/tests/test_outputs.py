"""Three-layer verifier for orch-compact."""

from _lib import orch_assert as oa

APP = oa.APP
ALLOWLIST = ["report.txt", "bench-orchestration.yaml"]


def _expected_report():
    lines = []
    for path in sorted((APP / "data").glob("*.txt")):
        first = path.read_text().split()[0]
        lines.append(f"{path.name}: {first}")
    return "\n".join(lines)


def test_end_state():
    report = APP / "report.txt"
    assert report.is_file(), "report.txt is missing"
    got = "\n".join(line for line in report.read_text().splitlines() if line.strip())
    assert got == _expected_report(), f"report mismatch:\n--- got ---\n{got}"


def test_workspace_integrity():
    oa.assert_no_unexpected_changes(oa.seed(), allowlist=ALLOWLIST)
    oa.assert_byte_identical(oa.seed(), "trap.txt")


def test_orchestration_trace():
    """Compaction kept call/result pairs and rolled the context over."""
    oa.assert_tool_call_linkage()
    oa.assert_message_count_below(45)
