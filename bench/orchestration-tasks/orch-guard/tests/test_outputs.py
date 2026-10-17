"""Three-layer verifier for orch-guard."""

from _lib import orch_assert as oa

APP = oa.APP
ALLOWLIST = ["answer.txt", "ok.txt", "bench-orchestration.yaml"]


def test_end_state():
    """The retry loop converged and the guard warning was reported."""
    assert (APP / "ok.txt").is_file(), "flaky.sh never succeeded"

    answer = (APP / "answer.txt").read_text()
    assert "attempts:" in answer, f"answer.txt does not report attempts:\n{answer}"
    assert "WARNING: error rate" in answer, (
        f"answer.txt does not carry the supervisor warning:\n{answer}"
    )


def test_workspace_integrity():
    oa.assert_no_unexpected_changes(oa.seed(), allowlist=ALLOWLIST)
    oa.assert_byte_identical(oa.seed(), "flaky.sh")
    oa.assert_byte_identical(oa.seed(), "trap.txt")


def test_orchestration_trace():
    """The sub-agent hit repeated failures and received the guard guidance."""
    oa.assert_delegate_count(minimum=1)
    oa.assert_tool_failures(minimum=4)
    oa.assert_message_contains("WARNING: error rate")
    oa.assert_no_orphan_worktrees()
