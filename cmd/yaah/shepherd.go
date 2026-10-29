package yaah

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherdgit "github.com/buchenberg/goatherd-runtime/sandbox/git"
	"github.com/spf13/cobra"

	"github.com/buchenberg/yaah/internal/agent/recovery"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/tools"
)

// shepherdCmd surfaces the supervision state's leftovers (adoption
// plan R3): `yaah shepherd` lists the orphan report recovery produces,
// and `yaah shepherd prune <path>` deletes exactly one orphaned
// worktree — the only deletion path in the whole design, by name,
// user-invoked (D5: an orphaned worktree may hold the only copy of
// unmerged agent work, so nothing else may ever delete it).
var shepherdCmd = &cobra.Command{
	Use:   "shepherd",
	Short: "List and prune orphaned supervision worktrees and containers",
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

var shepherdContainersCmd = &cobra.Command{
	Use:   "containers",
	Short: "List orphaned per-dispatch containers",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		address, namespace, err := shepherdContainerTarget()
		if err != nil {
			return err
		}
		orphans, err := tools.ListOrphanContainers(context.Background(), address, namespace)
		if err != nil {
			return fmt.Errorf("shepherd: list containers: %w", err)
		}
		if len(orphans) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no orphaned containers.")
			return nil
		}
		fmt.Fprintf(cmd.OutOrStdout(), "orphaned containers (namespace %s):\n", namespace)
		for _, o := range orphans {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s  %s  created %s — prune with `yaah shepherd prune-container %s`\n",
				o.ID, o.Image, o.CreatedAt.Format(time.RFC3339), o.ID)
		}
		return nil
	},
}

var shepherdPruneContainerCmd = &cobra.Command{
	Use:   "prune-container <container-id>",
	Short: "Delete one orphaned per-dispatch container (exact id)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		address, namespace, err := shepherdContainerTarget()
		if err != nil {
			return err
		}
		orphans, err := tools.ListOrphanContainers(context.Background(), address, namespace)
		if err != nil {
			return fmt.Errorf("shepherd: list containers: %w", err)
		}
		for _, o := range orphans {
			if o.ID != args[0] {
				continue
			}
			if err := tools.PruneContainer(context.Background(), address, namespace, o.ID); err != nil {
				return fmt.Errorf("shepherd: prune-container %s: %w", o.ID, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "pruned container %s\n", o.ID)
			return nil
		}
		return fmt.Errorf("shepherd: %s is not on the orphaned-container list — run `yaah shepherd containers`; only listed ids prune", args[0])
	},
}

// shepherdContainerTarget resolves the daemon and namespace for the container
// listing. The address is required: without it the containerd client has no
// endpoint to dial, and an empty address must not silently become a different
// daemon than the session used.
func shepherdContainerTarget() (address, namespace string, err error) {
	cfg, err := config.Load()
	if err != nil {
		return "", "", fmt.Errorf("shepherd: load config: %w", err)
	}
	address = strings.TrimSpace(cfg.Workspace.Containerd.Address)
	if address == "" {
		return "", "", errors.New("shepherd: workspace.containerd.address is required to inspect containers")
	}
	namespace = strings.TrimSpace(cfg.Workspace.Containerd.Namespace)
	if namespace == "" {
		namespace = "default"
	}
	return address, namespace, nil
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
		return pruneOrphan(cmd.OutOrStdout(), report, args[0])
	},
}

// shepherdScan recovers the supervision state from the configured
// trace store — the same scan startup and doctor run — and returns its
// report. Tracing is integral, so there is no off state to report.
func shepherdScan() (*recovery.Report, func(), error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("shepherd: load config: %w", err)
	}
	tracePath := cfg.Agent.Default.TracePath()
	if err := os.MkdirAll(filepath.Dir(tracePath), 0o700); err != nil {
		return nil, nil, fmt.Errorf("shepherd: trace dir: %w", err)
	}
	store, err := shepherdtrace.NewSQLiteTraceStore(tracePath)
	if err != nil {
		return nil, nil, fmt.Errorf("shepherd: trace store: %w", err)
	}
	cleanup := func() { store.Close(context.Background()) }
	repoPath := cfg.Agent.Default.RetainedRepoPath
	if repoPath == "" {
		repoPath, _ = os.Getwd()
	}
	_, report, rerr := recovery.Recover(context.Background(), store, repoPath, true, nil)
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
		fmt.Fprintf(w, "  orphaned %s (%s) — scope %s; prunable with `yaah shepherd prune \"%s\"`\n", o.Path, formatSize(o.Size), o.ScopeID, o.Path)
	}
	for _, m := range report.Missing {
		fmt.Fprintf(w, "  missing  %s — scope %s; the worktree is gone, its work is lost\n", m.Path, m.ScopeID)
	}
	for _, u := range report.Unadopted {
		fmt.Fprintf(w, "  unadopted %s — scope %s; not a registered worktree of its repository\n", u.Path, u.ScopeID)
	}
}

// pruneOrphan deletes exactly one orphaned worktree. The exact path
// is required — recovery's orphan report names it — and everything
// else refuses by its own name: adopted worktrees are live work,
// missing ones have nothing to delete, unadopted ones are not yaah's
// to remove, and an unlisted path is not on the report at all.
//
// Deletion itself verifies ownership first: git's worktree registry
// must still list the path AND the directory's .git file must point
// under the recorded repository's worktree administration — the
// registry keeps stale entries, a directory can be replaced with
// unrelated data, and a user-supplied path is not ownership
// verification. A registry lookup failure refuses rather than
// falling back to deleting the directory.
func pruneOrphan(w io.Writer, report *recovery.Report, name string) error {
	if report == nil {
		return errors.New("shepherd: nothing to prune (tracing off)")
	}
	for _, o := range report.Orphans {
		if o.Path != name {
			continue
		}
		registered, err := registeredWorktree(context.Background(), o.Repo, name)
		if err != nil {
			return fmt.Errorf("shepherd: prune %s: worktree registry unavailable: %w", name, err)
		}
		if !registered || !recovery.WorktreeOwnedBy(o.Repo, name) {
			return fmt.Errorf("shepherd: prune %s: the directory is no longer a worktree of its recorded repository — refusing; remove it manually if you are sure", name)
		}
		if err := goatherdgit.NewWorktreeSandbox(o.Repo, name).Destroy(context.Background()); err != nil {
			return fmt.Errorf("shepherd: prune %s: %w", name, err)
		}
		fmt.Fprintf(w, "pruned %s (scope %s)\n", name, o.ScopeID)
		return nil
	}
	for _, p := range report.Adopted {
		if p == name {
			return fmt.Errorf("shepherd: %s is an adopted (live) worktree — pruning refuses; it holds unmerged work in use", name)
		}
	}
	for _, m := range report.Missing {
		if m.Path == name {
			return fmt.Errorf("shepherd: %s is reported missing — the worktree is already gone; there is nothing to delete", name)
		}
	}
	for _, u := range report.Unadopted {
		if u.Path == name {
			return fmt.Errorf("shepherd: %s is reported unadopted — it is not a registered worktree of its repository, so it is not yaah's to remove; do it manually if you are sure", name)
		}
	}
	return fmt.Errorf("shepherd: %s is not on the orphan report — run `yaah shepherd` to list orphans; only listed paths prune", name)
}

// registeredWorktree reports whether git's registry currently lists
// path as a worktree of repo, distinguishing a failed lookup (an
// error: the registry is unverifiable, so deletion must refuse) from
// a genuine not-listed.
func registeredWorktree(ctx context.Context, repo, name string) (bool, error) {
	paths, err := recovery.RegisteredWorktreePaths(ctx, repo)
	if err != nil {
		return false, err
	}
	for _, p := range paths {
		if p == name {
			return true, nil
		}
		// Registry paths and the recorded path can differ lexically
		// (symlinked temp dirs, 8.3 short names) — compare the
		// directories themselves.
		ps, perr := os.Stat(p)
		ns, nerr := os.Stat(name)
		if perr == nil && nerr == nil && os.SameFile(ps, ns) {
			return true, nil
		}
	}
	return false, nil
}

func init() {
	shepherdCmd.AddCommand(shepherdContainersCmd)
	shepherdCmd.AddCommand(shepherdPruneCmd)
	shepherdCmd.AddCommand(shepherdPruneContainerCmd)
	rootCmd.AddCommand(shepherdCmd)
}
