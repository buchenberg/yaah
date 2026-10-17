"""Three-layer verifier for orch-fanout."""

import re

from _lib import orch_assert as oa

APP = oa.APP
INVENTORY = APP / "inventory"
AUDITS = APP / "audits"

ALLOWLIST = ["audits", "bench-orchestration.yaml"]


def _expected_audits():
    expected = {}
    for module in sorted(INVENTORY.glob("*.py")):
        if module.name == "__init__.py":
            continue
        lines = module.read_text().splitlines()
        functions = [
            match.group(1)
            for line in lines
            if (match := re.match(r"def ([A-Za-z]\w*)\(", line))
        ]
        bare_except = any(re.match(r"\s*except\s*:", line) for line in lines)
        expected[module.stem + ".md"] = "\n".join(
            [
                f"module: {module.name}",
                f"lines: {len(lines)}",
                f"functions: {', '.join(functions) or 'none'}",
                f"bare_except: {'yes' if bare_except else 'no'}",
            ]
        )
    return expected


def test_end_state():
    """Layer 1: exactly the six audits, in the canonical format."""
    expected = _expected_audits()
    assert AUDITS.is_dir(), "no /app/audits directory was produced"

    actual = sorted(path.name for path in AUDITS.iterdir() if path.is_file())
    assert actual == sorted(expected), (
        f"audit files differ: got {actual}, want {sorted(expected)}"
    )

    for name, want in expected.items():
        got = (AUDITS / name).read_text().strip()
        assert got == want, f"{name} content mismatch:\n--- got ---\n{got}\n--- want ---\n{want}"


def test_workspace_integrity():
    """Layer 2: the seeded package and trap file are untouched."""
    oa.assert_no_unexpected_changes(oa.seed(), allowlist=ALLOWLIST)
    oa.assert_byte_identical(oa.seed(), "trap.txt")


def test_orchestration_trace():
    """Layer 3: the work was fanned out to parallel sub-agents."""
    oa.assert_delegate_count(minimum=3)
    oa.assert_parallel_dispatch(minimum=3)
    oa.assert_sub_token_share()
    oa.assert_no_orphan_worktrees()
