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
	"os/exec"
	"path/filepath"
	"runtime"
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

// worktreeInfo is one recorded worktree provisioning: where it lives
// and which repository it was carved from. The repository is what
// makes adoption safe — an unrelated directory squatting on the path
// is not a worktree of that repo and is never adopted, because
// GitSandbox.Destroy falls back to RemoveAll and would delete user
// data.
type worktreeInfo struct {
	path string
	repo string
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

// Unadopted is an active worktree child whose recorded path exists but
// was not re-adopted — an unregistered occupant (the strict resolver
// refuses squatters), or a git-registry lookup failure. The scope
// recovers causal-only; any work in the directory is unreachable
// through the settlement, so it is reported rather than silenced.
type Unadopted struct {
	ScopeID string
	Path    string
}

// Report summarizes one recovery for the caller's startup output.
type Report struct {
	Scopes    int
	Roots     int
	Adopted   int // worktree children re-adopted (their work is live again)
	Orphans   []Orphan
	Missing   []Missing
	Unadopted []Unadopted
}

// Recover rebuilds the ScopeManager from the trace's durable records
// (adoption plan R1, D5):
//
//   - git roots re-adopt the in-place repository (repoPath) — but only
//     when adoptRoots is set: an isolated session's root is
//     deliberately pure-causal (the container is the substrate, D2),
//     and re-attaching a host git sandbox on restart would lie about
//     the tree exactly the way creation refused to. The kernel passes
//     the same empty backend for every root, so the host's session
//     mode is the only discriminator. Known limit: past sessions of
//     the OTHER mode in the same trace adopt with the current
//     session's rule — their roots are data, nothing dispatches
//     through them;
//   - worktree children re-adopt by the recorded path when it is on
//     disk — a crash mid-fork leaves live work recoverable — and land
//     on the orphan or missing report when the scope is terminal or
//     the path is gone;
//   - containerd scopes recover causal-only (the backend records no
//     re-adoptable identity yet).
//
// A resolver error fails the recovery loudly: half-adopted sandboxes
// are worse than none (the kernel's contract).
func Recover(ctx context.Context, store *shepherdtrace.SQLiteTraceStore, repoPath string, adoptRoots bool) (*shepherd.ScopeManager, *Report, error) {
	if repoPath == "" {
		return nil, nil, fmt.Errorf("no repository to re-adopt onto")
	}
	paths, err := scanWorktreePaths(ctx, store)
	if err != nil {
		return nil, nil, err
	}

	resolver := func(scopeID, backend string) (shepherdsandbox.Sandbox, error) {
		switch {
		case backend == "":
			// Roots resolve with an empty backend by the kernel's
			// recovery contract ("empty for roots, which have no
			// record") — not the "git" both local sandboxes report.
			// The plan re-adopts the in-place git repository — unless
			// the session is isolated (adoptRoots off), where a host
			// git sandbox would break D2's pure-causal root.
			if !adoptRoots {
				return nil, nil
			}
			return shepherdsandbox.NewLocalGitSandbox(repoPath), nil
		case backend == "containerd":
			// Deliberate non-adoption (D5): no re-adoptable identity.
			return nil, nil
		case backend == "git":
			// An isolated worktree child. Adoption is strict: the
			// recorded path must be a worktree registered with the
			// recorded repository — the trace directory is shared
			// across runs, an unrelated directory can squat on the
			// path, and Destroy falls back to RemoveAll, so a squatter
			// adopted as an owned sandbox is user data a later
			// teardown would delete. Anything less stays unadopted;
			// the classification below reports every refusal outcome.
			owner := strings.TrimPrefix(scopeID, "scope:")
			info, ok := paths[owner]
			if !ok {
				return nil, nil
			}
			// On disk AND registered: `git worktree list` keeps stale
			// administrative entries after the directory is gone, so
			// the registry alone would adopt a phantom.
			if _, serr := os.Stat(info.path); serr != nil {
				return nil, nil
			}
			if !IsRegisteredWorktree(ctx, info.repo, info.path) {
				return nil, nil
			}
			return shepherdsandbox.NewWorktreeSandbox(info.repo, info.path), nil
		default:
			return nil, nil
		}
	}

	mgr, err := shepherd.RecoverScopes(store, shepherd.RecoverOptions{Resolver: resolver})
	if err != nil {
		return nil, nil, err
	}

	// Orphans stay inert (warn-only means never deletable): a terminal
	// scope's worktree must not carry an owned sandbox handle a later
	// kernel teardown path would destroy it through. The resolver is
	// state-blind by contract, so detach what it attached to terminal
	// scopes here.
	for owner := range paths {
		scope, ok := mgr.Get("scope:" + owner)
		if !ok || scope.State() == shepherd.ScopeActive {
			continue
		}
		if scope.Sandbox() != nil {
			scope.WithSandbox(nil, false)
		}
	}

	report := &Report{}
	for _, scope := range mgr.AllScopes() {
		report.Scopes++
		if scope.Parent() == nil {
			report.Roots++
		}
	}
	for owner, info := range paths {
		scope, ok := mgr.Get("scope:" + owner)
		if !ok {
			// A record whose fork failed before it happened; the
			// provisioning path destroys the worktree in that case.
			continue
		}
		_, statErr := os.Stat(info.path)
		onDisk := statErr == nil
		switch {
		case !onDisk && scope.State() == shepherd.ScopeActive:
			report.Missing = append(report.Missing, Missing{ScopeID: scope.ID(), Path: info.path})
		case onDisk && scope.State() == shepherd.ScopeActive:
			// Adopted means adopted: the strict resolver refuses an
			// unregistered occupant, and the count must not claim it —
			// the refusal is reported as unadopted instead.
			if scope.Sandbox() != nil {
				report.Adopted++
			} else {
				report.Unadopted = append(report.Unadopted, Unadopted{ScopeID: scope.ID(), Path: info.path})
			}
		case onDisk:
			// Terminal scope, worktree still standing: unmerged work
			// may live in it. List it, never delete it.
			report.Orphans = append(report.Orphans, Orphan{ScopeID: scope.ID(), Path: info.path, Size: dirSize(info.path)})
		}
	}
	return mgr, report, nil
}

// scanWorktreePaths reads every worktree-provision record into an
// owner → info map (the last record wins — paths are deterministic per
// child). A record without a usable path or repo is a corrupt registry
// entry — the exact "a worktree the trace cannot find is an orphan
// recovery would never list" failure this design fights — and fails
// the recovery loudly rather than being skipped.
func scanWorktreePaths(ctx context.Context, store *shepherdtrace.SQLiteTraceStore) (map[string]worktreeInfo, error) {
	records, err := shepherdtrace.ScanRecordsBySchema(ctx, store, SchemaWorktreeProvisioned)
	if err != nil {
		return nil, fmt.Errorf("scan worktree records: %w", err)
	}
	paths := make(map[string]worktreeInfo, len(records))
	for _, rec := range records {
		path, pok := rec.Payload["path"].(string)
		repo, rok := rec.Payload["repo"].(string)
		if !pok || path == "" || !rok || repo == "" {
			return nil, fmt.Errorf("worktree record %s on %s has no usable path/repo", rec.RecordID, rec.OwnerID)
		}
		paths[rec.OwnerID] = worktreeInfo{path: path, repo: repo}
	}
	return paths, nil
}

// IsRegisteredWorktree reports whether path is currently registered as
// a git worktree of repoPath. A worktree left behind by a killed run
// still appears in `git worktree list`; unrelated data in a
// user-configurable worktree root does not — which is what makes both
// clearing a stale path (provisioning) and adopting a recovered one
// (recovery) safe: the git registry, not the filesystem, decides
// what yaah may own or destroy.
func IsRegisteredWorktree(ctx context.Context, repoPath, path string) bool {
	out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return false
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	wantStat, statErr := os.Stat(abs)
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		wt, err := filepath.Abs(strings.TrimPrefix(line, "worktree "))
		if err != nil {
			continue
		}
		// Windows paths differ in case; a lexical compare there would
		// miss the registered entry. Temp dirs also differ lexically
		// on other platforms — macOS resolves /var through a symlink
		// to /private/var, Windows may hand out an 8.3 short name —
		// so after the string compare, os.SameFile compares the
		// directories themselves, which no naming quirk can fool.
		if runtime.GOOS == "windows" {
			if strings.EqualFold(filepath.Clean(wt), filepath.Clean(abs)) {
				return true
			}
		} else if filepath.Clean(wt) == filepath.Clean(abs) {
			return true
		}
		if statErr == nil {
			if wtStat, werr := os.Stat(wt); werr == nil && os.SameFile(wantStat, wtStat) {
				return true
			}
		}
	}
	return false
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
