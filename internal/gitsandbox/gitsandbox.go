// Package gitsandbox constructs yaah's in-place git sandboxes for the
// shepherd kernel.
//
// It wraps the kernel's GitSandbox so Apply (rewind) never deletes
// untracked files that the target state does not contain. The kernel's
// apply sequence runs `git clean -fd`, which wipes everything created
// after the checkpoint was captured — including orchestrator scratch
// files, freshly authored role definitions, and sub-agent output
// directories that the rewind was never meant to reclaim (issue #313).
// Captured untracked content is part of the checkpoint tree and round-
// trips through Apply unchanged; tracked content follows the kernel's
// exact reset semantics. The wrapper only rescues files that exist now
// and are absent from the target state.
//
// Upstream note: the deletion lives in the kernel's applyTree/applyState
// (shepherd-kernel-go sandbox/git/backend.go, cleanFiles). If the kernel
// ever makes the clean policy host-selectable, this wrapper can retire.
package gitsandbox

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	shepherdsandbox "github.com/buchenberg/shepherd-kernel-go/sandbox"
	shepherdgit "github.com/buchenberg/shepherd-kernel-go/sandbox/git"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"
)

// NewLocalSandbox returns an in-place sandbox over repoPath whose Apply
// preserves untracked files absent from the applied state. All other
// operations delegate to the kernel GitSandbox unchanged.
func NewLocalSandbox(repoPath string) shepherdsandbox.Sandbox {
	return &untrackedPreserving{
		GitSandbox: shepherdgit.NewLocalGitSandbox(repoPath),
		repoPath:   repoPath,
	}
}

// untrackedPreserving decorates an in-place GitSandbox. Embedding the
// concrete type keeps the optional kernel capabilities (DeltaApplier,
// DeltaBaseAnnotator, Addressed) satisfied through promotion; only Apply
// changes behavior.
type untrackedPreserving struct {
	*shepherdgit.GitSandbox
	repoPath string
}

var _ shepherdsandbox.Sandbox = (*untrackedPreserving)(nil)

// stashedFile records one evacuated path and where its content waits.
type stashedFile struct {
	rel     string // repo-relative path
	stashed string // absolute path inside the stash dir
}

// Apply resets the workspace to ws like the kernel sandbox does, but
// untracked files the target state does not contain are evacuated into
// the repository's git dir before the destructive apply and moved back
// afterwards. Files present in the target tree are captured content and
// are left to the kernel's restore. On any failure the evacuation is
// undone best-effort so a failed rewind cannot strand user files.
func (u *untrackedPreserving) Apply(ctx context.Context, ws shepherdtrace.WorkspaceState) error {
	// Deliberately uncancellable, matching the kernel's own git runner:
	// an interrupted rewind must still restore evacuated files rather
	// than strand them in the stash.
	ctx = context.WithoutCancel(ctx)

	stashDir, stashed, err := u.evacuate(ctx, ws)
	if stashDir != "" {
		defer os.RemoveAll(stashDir)
	}
	if err != nil {
		// Evacuation failed before anything was moved or applied; undo
		// any partial evacuation and surface the error.
		restoreStashed(u.repoPath, stashed)
		return err
	}

	applyErr := u.GitSandbox.Apply(ctx, ws)
	restoreStashed(u.repoPath, stashed)
	return applyErr
}

// evacuate moves every untracked (non-ignored) file that the target
// state does not contain into a fresh stash directory under the git dir
// — inside .git so the kernel's `git clean -fd` cannot reach it, and on
// the same filesystem so renames are cheap. It returns the stash dir
// ("" when nothing needed evacuating) and the moved-file records.
func (u *untrackedPreserving) evacuate(ctx context.Context, ws shepherdtrace.WorkspaceState) (string, []stashedFile, error) {
	untracked, err := u.gitLines(ctx, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", nil, fmt.Errorf("preserve untracked: list untracked files: %w", err)
	}
	if len(untracked) == 0 {
		return "", nil, nil
	}

	// The target tree's paths are captured content the kernel restores
	// itself; evacuating them would race the apply. Legacy stash-identified
	// states have no enumerable tree, so everything untracked is preserved
	// and restoreStashed's exists-check keeps applied content authoritative.
	inTarget := map[string]bool{}
	if treeSHA, _ := ws.Data["tree_sha"].(string); treeSHA != "" {
		paths, err := u.gitLines(ctx, "ls-tree", "-r", "--name-only", "-z", treeSHA)
		if err != nil {
			return "", nil, fmt.Errorf("preserve untracked: list target tree %s: %w", treeSHA, err)
		}
		for _, p := range paths {
			inTarget[p] = true
		}
	}

	var evac []string
	for _, p := range untracked {
		if !inTarget[p] {
			evac = append(evac, p)
		}
	}
	if len(evac) == 0 {
		return "", nil, nil
	}

	gitDir, err := u.gitOneLine(ctx, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", nil, fmt.Errorf("preserve untracked: resolve git dir: %w", err)
	}
	stashDir, err := os.MkdirTemp(gitDir, "shepherd-untracked-*")
	if err != nil {
		return "", nil, fmt.Errorf("preserve untracked: create stash dir: %w", err)
	}

	stashed := make([]stashedFile, 0, len(evac))
	for _, rel := range evac {
		src := filepath.Join(u.repoPath, rel)
		dst := filepath.Join(stashDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return stashDir, stashed, fmt.Errorf("preserve untracked: stash dir for %s: %w", rel, err)
		}
		if err := os.Rename(src, dst); err != nil {
			if os.IsNotExist(err) {
				continue // vanished between listing and stashing
			}
			return stashDir, stashed, fmt.Errorf("preserve untracked: stash %s: %w", rel, err)
		}
		stashed = append(stashed, stashedFile{rel: rel, stashed: dst})
	}
	return stashDir, stashed, nil
}

// restoreStashed moves evacuated files back, skipping any path that now
// exists (the applied state wins; for tree states this cannot happen by
// construction, for legacy stash states it keeps the restore idempotent).
// Best-effort: it never returns an error so it cannot mask the apply
// result, but a failed move is reported on stderr — the content still
// exists inside the stash dir, whose removal is deferred by the caller.
func restoreStashed(repoPath string, stashed []stashedFile) {
	for _, f := range stashed {
		dst := filepath.Join(repoPath, f.rel)
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		err := os.MkdirAll(filepath.Dir(dst), 0o755)
		if err == nil {
			err = os.Rename(f.stashed, dst)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not restore untracked file %s after workspace rewind: %v\n", f.rel, err)
		}
	}
}

// gitLines runs a git command in the repository and splits NUL-delimited
// output into non-empty lines.
func (u *untrackedPreserving) gitLines(ctx context.Context, args ...string) ([]string, error) {
	out, err := u.git(ctx, args...)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\x00") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// gitOneLine runs a git command and returns its single trimmed output line.
func (u *untrackedPreserving) gitOneLine(ctx context.Context, args ...string) (string, error) {
	out, err := u.git(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (u *untrackedPreserving) git(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = u.repoPath
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
