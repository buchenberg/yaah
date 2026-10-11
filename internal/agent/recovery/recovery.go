// Package recovery rebuilds the shepherd scope registry from the
// durable trace at startup (adoption plan R1, decision D5): the trace
// is the registry — scope lifecycle events, checkpoint blobs, and
// settlement records are durably recorded, so a restarted process
// recovers its supervision state instead of forgetting it.
//
// Orphans are warn-only (D5, settled): an unadopted worktree may hold
// the only copy of unmerged agent work, so recovery lists it and
// leaves it on disk. Nothing is ever deleted here — the only deletion
// path is an explicit user command (R3's prune).
package recovery

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	shepherdsandbox "github.com/buchenberg/shepherd-kernel-go/sandbox"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"
)

// SchemaWorktreeProvisioned records where yaah provisioned an isolated
// worktree child, on the child's owner path. The kernel's fork record
// carries the backend but not the path — and re-adoption needs the
// path — so yaah records it itself; the trace is the registry. A child
// without a record recovers causal-only (deliberate non-adoption, not
// an error).
const SchemaWorktreeProvisioned = "yaah.worktree.provisioned.v1"

// RecordWorktree appends the worktree-path record for one provisioned
// isolated child. Callers run it right after the isolated fork
// succeeds; a failure must discard the branch — a worktree the trace
// cannot find is an orphan recovery would never list.
func RecordWorktree(ctx context.Context, store *shepherdtrace.SQLiteTraceStore, owner, repoPath, worktreePath string) error {
	if store == nil {
		return fmt.Errorf("no trace store")
	}
	_, err := store.Append(ctx, shepherdtrace.TrustedAppendContext, shepherdtrace.AppendBatch{
		AppendIntentID: fmt.Sprintf("%s:worktree:%d", owner, time.Now().UnixNano()),
		Groups: []shepherdtrace.AppendGroup{{
			TraceOwnerID: owner,
			FactDrafts: []shepherdtrace.RecordDraft{{
				Mode:      shepherdtrace.Declaration,
				SchemaRef: SchemaWorktreeProvisioned,
				KindLabel: "worktree:provisioned",
				Payload: map[string]any{
					"path": worktreePath,
					"repo": repoPath,
				},
			}},
		}},
	})
	if err != nil {
		return fmt.Errorf("record worktree path for %s: %w", owner, err)
	}
	return nil
}

// Orphan is an unadopted worktree left on disk: the scope's work is
// terminal (merged or discarded — its lifecycle record outlived the
// crash window before the sandbox teardown), but the worktree still
// stands and may hold the only copy of unmerged work. Warn-only:
// recovery lists it and never deletes it.
type Orphan struct {
	ScopeID string
	Path    string
	Size    int64
}

// Missing is an active worktree child whose recorded path is gone: the
// crash lost its unmerged work. Reported with scope and path so the
// user knows what was lost; a terminal child whose worktree is gone is
// the expected post-teardown state and stays silent.
type Missing struct {
	ScopeID string
	Path    string
}

// Report summarizes one recovery for the caller's startup output.
type Report struct {
	Scopes  int
	Roots   int
	Adopted int // worktree children re-adopted (their work is live again)
	Orphans []Orphan
	Missing []Missing
}

// Recover rebuilds the ScopeManager from the trace's durable records
// (adoption plan R1, D5):
//
//   - git roots re-adopt the in-place repository (repoPath);
//   - worktree children re-adopt by the recorded path when it is on
//     disk — a crash mid-fork leaves live work recoverable — and land
//     on the orphan or missing report when the scope is terminal or
//     the path is gone;
//   - containerd scopes recover causal-only (the backend records no
//     re-adoptable identity yet).
//
// A resolver error fails the recovery loudly: half-adopted sandboxes
// are worse than none (the kernel's contract).
func Recover(ctx context.Context, store *shepherdtrace.SQLiteTraceStore, repoPath string) (*shepherd.ScopeManager, *Report, error) {
	paths, err := scanWorktreePaths(ctx, store)
	if err != nil {
		return nil, nil, err
	}

	resolver := func(scopeID, backend string) (shepherdsandbox.Sandbox, error) {
		switch {
		case backend == "":
			// Roots have no backend record; the plan re-adopts the
			// in-place git repository.
			return shepherdsandbox.NewLocalGitSandbox(repoPath), nil
		case backend == "containerd":
			// Deliberate non-adoption (D5): no re-adoptable identity.
			return nil, nil
		case backend == "git":
			// An isolated worktree child. Without a recorded path
			// there is nothing to find; without the path on disk
			// there is nothing to adopt — the classification below
			// reports both.
			owner := strings.TrimPrefix(scopeID, "scope:")
			path, ok := paths[owner]
			if !ok || path == "" {
				return nil, nil
			}
			if _, serr := os.Stat(path); serr != nil {
				return nil, nil
			}
			return shepherdsandbox.NewWorktreeSandbox(repoPath, path), nil
		default:
			return nil, nil
		}
	}

	mgr, err := shepherd.RecoverScopes(store, shepherd.RecoverOptions{Resolver: resolver})
	if err != nil {
		return nil, nil, err
	}

	report := &Report{}
	for _, scope := range mgr.AllScopes() {
		report.Scopes++
		if scope.Parent() == nil {
			report.Roots++
		}
	}
	for owner, path := range paths {
		scope, ok := mgr.Get("scope:" + owner)
		if !ok {
			// A record whose fork failed before it happened; the
			// provisioning path destroys the worktree in that case.
			continue
		}
		_, statErr := os.Stat(path)
		onDisk := statErr == nil
		switch {
		case !onDisk && scope.State() == shepherd.ScopeActive:
			report.Missing = append(report.Missing, Missing{ScopeID: scope.ID(), Path: path})
		case onDisk && scope.State() == shepherd.ScopeActive:
			report.Adopted++
		case onDisk:
			// Terminal scope, worktree still standing: unmerged work
			// may live in it. List it, never delete it.
			report.Orphans = append(report.Orphans, Orphan{ScopeID: scope.ID(), Path: path, Size: dirSize(path)})
		}
	}
	return mgr, report, nil
}

// scanWorktreePaths reads every worktree-provision record into an
// owner → path map (the last record wins — paths are deterministic
// per child).
func scanWorktreePaths(ctx context.Context, store *shepherdtrace.SQLiteTraceStore) (map[string]string, error) {
	records, err := shepherdtrace.ScanRecordsBySchema(ctx, store, SchemaWorktreeProvisioned)
	if err != nil {
		return nil, fmt.Errorf("scan worktree records: %w", err)
	}
	paths := make(map[string]string, len(records))
	for _, rec := range records {
		if path, ok := rec.Payload["path"].(string); ok && path != "" {
			paths[rec.OwnerID] = path
		}
	}
	return paths, nil
}

// dirSize sums a directory tree's file sizes — the orphan report's
// "how much is on the line" number.
func dirSize(path string) int64 {
	var total int64
	_ = fs.WalkDir(os.DirFS(path), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // best effort: the report is a warning, not an audit
		}
		if info, ierr := d.Info(); ierr == nil && !d.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}
