package agent

import "testing"

// TestBuildSeedsPreviousSummary pins the resume seam (yaah-an55): the
// builder carries the persisted anchored summary into the loop state, where
// the context manager reads it for the next compaction prompt.
func TestBuildSeedsPreviousSummary(t *testing.T) {
	const summary = "## Goal\n- resume hydration"
	l := (&LoopBuilder{Model: "m", PreviousSummary: summary}).Build(LoopBuildOptions{})
	if l.State.PreviousSummary != summary {
		t.Fatalf("State.PreviousSummary = %q, want the builder's summary", l.State.PreviousSummary)
	}
}
