"""Three-layer verifier for orch-quality-gate."""

import subprocess

from _lib import orch_assert as oa

APP = oa.APP
ALLOWLIST = ["mathx.py", "bench-orchestration.yaml"]


def test_end_state():
    result = subprocess.run(
        ["python3", "verify.py"], cwd=APP, capture_output=True, text=True
    )
    assert result.returncode == 0, f"verify.py failed:\n{result.stdout}{result.stderr}"


def test_workspace_integrity():
    oa.assert_no_unexpected_changes(oa.seed(), allowlist=ALLOWLIST)
    oa.assert_byte_identical(oa.seed(), "verify.py")
    oa.assert_byte_identical(oa.seed(), "trap.txt")


def test_orchestration_trace():
    """A developer ran, the tester gate failed it, and the orchestrator fixed it."""
    oa.assert_delegate_count(minimum=1)
    oa.assert_subagent_role("tester")
    oa.assert_message_contains("[quality-gate:FAIL]")
    oa.assert_no_orphan_worktrees()
