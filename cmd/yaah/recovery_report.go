package yaah

import (
	"fmt"
	"os"

	"github.com/buchenberg/yaah/internal/agent/recovery"
)

// printRecoveryReport surfaces one startup recovery to the user
// (adoption plan R1, decision D5). Everything here is warn-only: an
// orphaned worktree may hold the only copy of unmerged agent work, so
// it is listed and left on disk — the only deletion path is an
// explicit prune (R3).
func printRecoveryReport(report *recovery.Report) {
	if report == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "shepherd recovery: %d scopes (%d root(s)) rebuilt from the trace", report.Scopes, report.Roots)
	if report.Adopted > 0 {
		fmt.Fprintf(os.Stderr, ", %d worktree(s) re-adopted", report.Adopted)
	}
	fmt.Fprintln(os.Stderr)

	for _, o := range report.Orphans {
		fmt.Fprintf(os.Stderr, "warning: orphaned worktree left on disk (scope %s): %s (%s) — it may hold unmerged work; it is never deleted automatically\n", o.ScopeID, o.Path, formatSize(o.Size))
	}
	for _, m := range report.Missing {
		fmt.Fprintf(os.Stderr, "warning: scope %s's worktree is gone: %s — its unmerged work is unrecoverable\n", m.ScopeID, m.Path)
	}
}

// formatSize renders a byte count for the orphan report.
func formatSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
