"""Three-layer verifier for orch-settle-discard."""

import re

from _lib import orch_assert as oa

APP = oa.APP
ALLOWLIST = ["answer.txt", "bench-orchestration.yaml"]


def test_end_state():
    """The proposal was discarded: no worktree change reached /app."""
    assert not (APP / "fizzbuzz.py").exists(), "discarded proposal leaked fizzbuzz.py"

    answer = (APP / "answer.txt").read_text().strip()
    assert answer.startswith("discarded:"), f"answer.txt = {answer!r}"
    proposal_id = answer.split(":", 1)[1].strip()
    assert re.fullmatch(r"proposal:\d+:\d+", proposal_id), (
        f"answer.txt does not name a proposal id: {proposal_id!r}"
    )


def test_workspace_integrity():
    oa.assert_no_unexpected_changes(oa.seed(), allowlist=ALLOWLIST)
    oa.assert_byte_identical(oa.seed(), "trap.txt")


def test_orchestration_trace():
    """A proposal was created and consumed with the discard verb."""
    oa.assert_delegate_count(minimum=1)
    oa.assert_settle_verbs({"discarded"})
    proposal_id = (APP / "answer.txt").read_text().split(":", 1)[1].strip()
    assert f"out:{proposal_id}" in oa.settled_output_ids(), (
        f"answer.txt names {proposal_id!r}, which is not the settled output"
    )
    oa.assert_no_orphan_worktrees()
