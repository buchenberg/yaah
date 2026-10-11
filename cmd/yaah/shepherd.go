package yaah

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	shepherdsandbox "github.com/buchenberg/shepherd-kernel-go/sandbox"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"
	"github.com/spf13/cobra"

	"github.com/buchenberg/yaah/internal/agent/recovery"
	"github.com/buchenberg/yaah/internal/config"
)

// shepherdCmd surfaces the supervision state's leftovers (adoption
// plan R3): `yaah shepherd` lists the orphan report recovery produces,
// and `yaah shepherd prune <path>` deletes exactly one orphaned
// worktree — the only deletion path in the whole design, by name,
// user-invoked (D5: an orphaned worktree may hold the only copy of
// unmerged agent work, so nothing else may ever delete it).
var shepherdCmd = &cobra.Command{
	Use:   "shepherd",
	Short: "List and prune orphaned supervision worktrees",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		report, cleanup, err := shepherdScan()
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			return err
		}
		printShepherdReport(cmd.OutOrStdout(), report)
		return nil
	},
}

var shepherdPruneCmd = &cobra.Command{
	Use:   "prune <worktree-path>",
	Short: "Delete one orphaned worktree (the only deletion path; exact path)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		report, cleanup, err := shepherdScan()
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			return err
		}
		return pruneOrphan(report, args[0])
	},
}

// shepherdScan recovers the supervision state from the configured
// trace dir — the same scan startup and doctor run — and returns its
// report. Off tracing is not an error: the command reports it.
func shepherdScan() (*recovery.Report, func(), error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("shepherd: load config: %w", err)
	}
	dir := cfg.Agent.Default.ShepherdTraceDir
	if dir == "" {
		fmt.Fprintln(os.Stderr, "shepherd tracing is off (set agents.default.shepherd_trace_dir)")
		return nil, nil, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("shepherd: trace dir: %w", err)
	}
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(dir, "trace.sqlite"))
	if err != nil {
		return nil, nil, fmt.Errorf("shepherd: trace store: %w", err)
	}
	cleanup := func() { store.Close(context.Background()) }
	repoPath := cfg.Agent.Default.SupervisedRepoPath
	if repoPath == "" {
		repoPath, _ = os.Getwd()
	}
	_, report, rerr := recovery.Recover(context.Background(), store, repoPath, true)
	if rerr != nil {
		return nil, cleanup, fmt.Errorf("shepherd: recovery: %w", rerr)
	}
	return report, cleanup, nil
}

// printShepherdReport lists the recovery's leftover buckets.
func printShepherdReport(w io.Writer, report *recovery.Report) {
	if report == nil {
		return
	}
	fmt.Fprintf(w, "recovery rebuilt %d scope(s) (%d root(s))\n", report.Scopes, report.Roots)
	if len(report.Adopted) == 0 && len(report.Orphans) == 0 && len(report.Missing) == 0 && len(report.Unadopted) == 0 {
		fmt.Fprintln(w, "no worktree leftovers.")
		return
	}
	for _, p := range report.Adopted {
		fmt.Fprintf(w, "  adopted   %s (live work, leave it)\n", p)
	}
	for _, o := range report.Orphans {
		fmt.Fprintf(w, "  orphaned %s (%s) — scope %s; prunable with `yaah shepherd prune %s`\n", o.Path, formatSize(o.Size), o.ScopeID, o.Path)
	}
	for _, m := range report.Missing {
		fmt.Fprintf(w, "  missing  %s — scope %s; the worktree is gone, its work is lost\n", m.Path, m.ScopeID)
	}
	for _, u := range report.Unadopted {
		fmt.Fprintf(w, "  unadopted %s — scope %s; not a registered worktree of its repository\n", u.Path, u.ScopeID)
	}
}

// pruneOrphan deletes exactly one orphaned worktree. The exact path
// is required — recovery's orphan report names it — and anything not
// on that report refuses: adopted worktrees are live work, missing
// ones have nothing to delete, and an unlisted path is not yaah's to
// touch.
func pruneOrphan(report *recovery.Report, name string) error {
	if report == nil {
		return errors.New("shepherd: nothing to prune (tracing off)")
	}
	for _, o := range report.Orphans {
		if o.Path != name {
			continue
		}
		// A registered worktree gets the kernel's teardown against its
		// recorded repository (git administrative state included); an
		// unregistered leftover directory is removed directly — the
		// user named it, the report listed it, and this is the
		// design's one deletion path.
		if recovery.IsRegisteredWorktree(context.Background(), o.Repo, name) {
			if err := shepherdsandbox.NewWorktreeSandbox(o.Repo, name).Destroy(context.Background()); err != nil {
				return fmt.Errorf("shepherd: prune %s: %w", name, err)
			}
		} else if err := os.RemoveAll(name); err != nil {
			return fmt.Errorf("shepherd: prune %s: %w", name, err)
		}
		fmt.Printf("pruned %s (scope %s)\n", name, o.ScopeID)
		return nil
	}
	for _, p := range report.Adopted {
		if p == name {
			return fmt.Errorf("shepherd: %s is an adopted (live) worktree — pruning refuses; it holds unmerged work in use", name)
		}
	}
	return fmt.Errorf("shepherd: %s is not on the orphan report — run `yaah shepherd` to list orphans; only listed paths prune", name)
}

func init() {
	shepherdCmd.AddCommand(shepherdPruneCmd)
	rootCmd.AddCommand(shepherdCmd)
}
