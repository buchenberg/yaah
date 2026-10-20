"""Three-layer verifier for orch-report-fanout."""

import re

from _lib import orch_assert as oa

APP = oa.APP
SRC = APP / "src"
REPORTS = APP / "reports"

ALLOWLIST = ["reports", "bench-orchestration.yaml"]


def _expected_reports():
    expected = {}
    for module in sorted(SRC.glob("*.py")):
        lines = module.read_text().splitlines()
        functions = [
            match.group(1)
            for line in lines
            if (match := re.match(r"def ([A-Za-z]\w*)\(", line))
        ]
        expected[module.stem + ".md"] = "\n".join(
            [
                f"module: {module.name}",
                f"lines: {len(lines)}",
                f"functions: {', '.join(functions) or 'none'}",
            ]
        )
    return expected


def test_end_state():
    expected = _expected_reports()
    assert expected, "fixture produced no expected reports"
    for name, want in expected.items():
        got = (REPORTS / name)
        assert got.is_file(), f"missing report: {got}"
        # Comma spacing in "functions:" is presentation (the fanout task
        # pinned this same normalization); compare token-wise.
        got_lines = [line.strip() for line in got.read_text().strip().splitlines()]
        want_lines = want.splitlines()
        if len(got_lines) != 3 or got_lines[0] != want_lines[0] or got_lines[1] != want_lines[1]:
            raise AssertionError(
                f"{name} content mismatch:\ngot:\n{got.read_text()}\nwant:\n{want}"
            )
        got_funcs = [f.strip() for f in got_lines[2].split(":", 1)[1].split(",") if f.strip()]
        want_funcs = [f.strip() for f in want_lines[2].split(":", 1)[1].split(",") if f.strip()]
        assert got_funcs == want_funcs, (
            f"{name} functions mismatch: {got_funcs} vs {want_funcs}"
        )


def test_workspace_integrity():
    oa.assert_no_unexpected_changes(oa.seed(), allowlist=ALLOWLIST)
    oa.assert_byte_identical(oa.seed(), "trap.txt")
    oa.assert_byte_identical(oa.seed(), "README.md")
    oa.assert_no_orphan_worktrees()


def test_orchestration_trace():
    oa.assert_delegate_count(minimum=2)
    oa.assert_parallel_dispatch(minimum=2)
    oa.assert_sub_token_share()
    oa.assert_no_proposals()
