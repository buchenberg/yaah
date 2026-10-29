"""Three-layer verifier for orch-recovery."""

from pathlib import Path

from _lib import orch_assert as oa

APP = Path("/app")
WT = APP / ".orch-worktree" / "wt"


def test_end_state():
    answer = (APP / "recovery.txt").read_text().strip()
    assert answer == f"adopted: {WT}", f"recovery.txt = {answer!r}"
    assert (WT / "recovered.txt").read_text() == "survived the crash\n"
    assert not (APP / "recovered.txt").exists()


def test_workspace_integrity():
    oa.assert_no_unexpected_changes(
        oa.seed(), allowlist=["recovery.txt", "bench-orchestration.yaml"]
    )
    oa.assert_byte_identical(oa.seed(), "trap.txt")
    # The recovered worktree is live work; it must still be a registered
    # worktree of /app, not an orphan or a missing path.
    assert oa.worktree_registered(WT), f"{WT} is not a registered worktree"


def test_recovery_trace():
    assert oa.recovered_scope_exists("sub-recovery", WT)
    assert oa.sealed_output_count() >= 1
