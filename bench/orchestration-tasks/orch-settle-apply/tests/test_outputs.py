"""Three-layer verifier for orch-settle-apply."""

import subprocess

from _lib import orch_assert as oa

APP = oa.APP
ALLOWLIST = ["CHANGELOG.md", "calc.py", "verify.py", "bench-orchestration.yaml"]


def test_end_state():
    """The CHANGELOG edit and the applied proposal both landed."""
    changelog = (APP / "CHANGELOG.md").read_text()
    assert "- v2: power helper" in changelog, "host-side CHANGELOG edit is missing"

    calc = (APP / "calc.py").read_text()
    assert "def power(" in calc, "the delegate's power() change was not applied"

    verify = (APP / "verify.py").read_text()
    assert "power(2, 5)" in verify, (
        "the delegate's verify.py assertion was not applied"
    )

    result = subprocess.run(
        ["python3", "verify.py"], cwd=APP, capture_output=True, text=True
    )
    assert result.returncode == 0, f"verify.py failed:\n{result.stdout}{result.stderr}"


def test_workspace_integrity():
    oa.assert_no_unexpected_changes(oa.seed(), allowlist=ALLOWLIST)
    oa.assert_byte_identical(oa.seed(), "trap.txt")


def test_orchestration_trace():
    """The retained proposal was settled with apply, over a moved basis."""
    oa.assert_delegate_count(minimum=1)
    oa.assert_settle_verbs({"applied"})
    oa.assert_no_orphan_worktrees()
