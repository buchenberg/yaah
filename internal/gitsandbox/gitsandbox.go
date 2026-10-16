// Package gitsandbox constructs yaah's in-place git sandboxes for the
// shepherd kernel.
//
// It wraps the kernel's GitSandbox so Apply (rewind) never deletes
// untracked files that the target state does not contain. The kernel's
// apply sequence runs `git clean -fd`, which wipes everything created
// after the checkpoint was captured — including orchestrator scratch
// files, freshly authored role definitions, empty scratch directories,
// and sub-agent output directories that the rewind was never meant to
// reclaim (issue #313). Captured untracked content is part of the
// checkpoint tree and round-trips through Apply unchanged; tracked
// content follows the kernel's exact reset semantics. The wrapper only
// rescues content that exists now and is absent from the target state.
//
// Preservation is lossless-or-loud: a file that cannot be moved back —
// because the applied state put something else at its path, or because
// a parent component is no longer a plain directory — stays in the
// stash under the git dir and Apply reports both the stash location and
// the reason. Symlinked parents are never written through, so a
// rewind cannot relocate preserved content outside the repository.
//
// Upstream note: the deletion lives in the runtime's applyTree/applyState
// (shepherd-runtime-go sandbox/git/backend.go, cleanFiles). If the runtime
// ever makes the clean policy host-selectable, this wrapper can retire.
package gitsandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	shepherdtrace "github.com/buchenberg/shepherd-kernel-go"
	shepherdsandbox "github.com/buchenberg/shepherd-runtime-go/sandbox"
	shepherdgit "github.com/buchenberg/shepherd-runtime-go/sandbox/git"
)

// NewLocalSandbox returns an in-place sandbox over repoPath whose Apply
// preserves untracked content absent from the applied state. All other
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
	rel     string // repo-relative path, slash-separated (git's form)
	stashed string // absolute path inside the stash dir
}

// evacuation is everything Apply moved aside and must put back: file
// contents in the stash dir, plus directory shapes to recreate (empty
// untracked directories carry no content, so recording their paths is
// enough — `git clean -fd` removes them and the restore re-creates
// them).
type evacuation struct {
	stashDir string
	files    []stashedFile
	dirs     []string // repo-relative, slash-separated
}

func (e evacuation) empty() bool {
	return len(e.files) == 0 && len(e.dirs) == 0
}

// Apply resets the workspace to ws like the kernel sandbox does, but
// untracked content the target state does not contain is evacuated
// before the destructive apply and moved back afterwards. Content
// present in the target tree is captured state and is left to the
// kernel's restore.
//
// Preservation is lossless-or-loud: if anything cannot be moved back,
// the stash is KEPT (never removed with originals inside) and the
// returned error names each unrestored path and the stash location —
// even when the apply itself succeeded. A silent success over lost
// user content is the exact failure class this wrapper exists to
// prevent.
func (u *untrackedPreserving) Apply(ctx context.Context, ws shepherdtrace.WorkspaceState) error {
	// Deliberately uncancellable, matching the kernel's own git runner:
	// an interrupted rewind must still restore evacuated files rather
	// than strand them in the stash.
	ctx = context.WithoutCancel(ctx)

	ev, evacErr := u.evacuate(ctx, ws)
	if evacErr != nil {
		// Undo any partial evacuation before surfacing the error; the
		// workspace has not been touched yet.
		restoreErrs := restoreEvacuated(u.repoPath, ev)
		if ev.stashDir != "" && len(restoreErrs) == 0 {
			os.RemoveAll(ev.stashDir)
		}
		return errors.Join(evacErr, joinRestoreErrors(ev.stashDir, restoreErrs))
	}
	if ev.empty() {
		return u.GitSandbox.Apply(ctx, ws)
	}

	applyErr := u.GitSandbox.Apply(ctx, ws)
	restoreErrs := restoreEvacuated(u.repoPath, ev)
	if len(restoreErrs) > 0 {
		// Keep the stash: it holds the only copy of everything that
		// could not be moved back.
		return errors.Join(applyErr, joinRestoreErrors(ev.stashDir, restoreErrs))
	}
	if ev.stashDir != "" {
		os.RemoveAll(ev.stashDir)
	}
	return applyErr
}

// joinRestoreErrors bundles restoration failures into one error that
// always reports where the unrestored originals are kept.
func joinRestoreErrors(stashDir string, errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	joined := errors.Join(errs...)
	if stashDir != "" {
		return fmt.Errorf("preserved untracked content could not be fully restored — originals kept in %s: %w", stashDir, joined)
	}
	return fmt.Errorf("preserved untracked content could not be fully restored: %w", joined)
}

// evacuate moves every untracked (non-ignored) file that the target
// state does not contain into a fresh stash directory under the git
// dir — inside .git so the kernel's `git clean -fd` cannot reach it,
// and on the same filesystem so renames are cheap — and records the
// untracked directory shapes to recreate after the apply.
func (u *untrackedPreserving) evacuate(ctx context.Context, ws shepherdtrace.WorkspaceState) (evacuation, error) {
	var ev evacuation

	untracked, err := u.gitLines(ctx, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return ev, fmt.Errorf("preserve untracked: list untracked files: %w", err)
	}

	// The target tree's paths are captured content the kernel restores
	// itself; evacuating them would race the apply. Its directory
	// prefixes are shapes the apply re-materializes. Legacy
	// stash-identified states have no enumerable tree, so everything
	// untracked is preserved and restore's exists-check keeps applied
	// content authoritative.
	inTarget := map[string]bool{}
	targetDirs := map[string]bool{}
	if treeSHA, _ := ws.Data["tree_sha"].(string); treeSHA != "" {
		paths, err := u.gitLines(ctx, "ls-tree", "-r", "--name-only", "-z", treeSHA)
		if err != nil {
			return ev, fmt.Errorf("preserve untracked: list target tree %s: %w", treeSHA, err)
		}
		for _, p := range paths {
			inTarget[p] = true
			for d := path.Dir(p); d != "." && d != "/"; d = path.Dir(d) {
				targetDirs[d] = true
			}
		}
	}

	for _, p := range untracked {
		if !inTarget[p] {
			ev.files = append(ev.files, stashedFile{rel: p})
		}
	}

	// Empty directories never appear in `ls-files --others`, so walk the
	// collapsed untracked directory roots (--directory) and record every
	// directory shape the target tree will not recreate. Directories
	// holding evacuated files are re-created by the file restore; this
	// covers the purely-empty ones and empty descendants of populated
	// directories (review finding on #313).
	roots, err := u.gitLines(ctx, "ls-files", "--others", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return ev, fmt.Errorf("preserve untracked: list untracked directories: %w", err)
	}
	for _, r := range roots {
		if !strings.HasSuffix(r, "/") {
			continue
		}
		root := strings.TrimSuffix(r, "/")
		rootFS := filepath.Join(u.repoPath, filepath.FromSlash(root))
		filepath.WalkDir(rootFS, func(p string, d fs.DirEntry, werr error) error {
			if werr != nil || p == rootFS && !d.IsDir() {
				return nil
			}
			if !d.IsDir() {
				return nil
			}
			rel, rerr := filepath.Rel(u.repoPath, p)
			if rerr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if rel == "." || targetDirs[rel] {
				return nil
			}
			ev.dirs = append(ev.dirs, rel)
			return nil
		})
	}

	if len(ev.files) == 0 {
		return ev, nil
	}
	filesToStash := ev.files

	gitDir, err := u.gitOneLine(ctx, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return evacuation{}, fmt.Errorf("preserve untracked: resolve git dir: %w", err)
	}
	stashDir, err := os.MkdirTemp(gitDir, "shepherd-untracked-*")
	if err != nil {
		return evacuation{}, fmt.Errorf("preserve untracked: create stash dir: %w", err)
	}
	ev.stashDir = stashDir

	evacuated := ev.files[:0]
	ev.files = nil
	for _, f := range filesToStash {
		src := filepath.Join(u.repoPath, filepath.FromSlash(f.rel))
		dst := filepath.Join(stashDir, filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			ev.files = evacuated
			return ev, fmt.Errorf("preserve untracked: stash dir for %s: %w", f.rel, err)
		}
		if err := os.Rename(src, dst); err != nil {
			ev.files = evacuated
			if os.IsNotExist(err) {
				continue // vanished between listing and stashing
			}
			return ev, fmt.Errorf("preserve untracked: stash %s: %w", f.rel, err)
		}
		f.stashed = dst
		evacuated = append(evacuated, f)
	}
	ev.files = evacuated
	return ev, nil
}

// restoreEvacuated recreates preserved directory shapes and moves
// evacuated files back. It never writes through a path component that
// is not a plain directory (the applied state may have reinstated a
// symlink or a file where a preserved directory used to be — following
// it could relocate content outside the repository), and never
// overwrites anything the apply produced. Every refusal is reported as
// an error naming the kept original; callers retain the stash when any
// error is returned.
func restoreEvacuated(repoPath string, ev evacuation) []error {
	var errs []error

	for _, d := range ev.dirs {
		p := filepath.Join(repoPath, filepath.FromSlash(d))
		if !parentsSafe(repoPath, d) {
			errs = append(errs, fmt.Errorf("directory %s not recreated: a parent path component is not a plain directory", d))
			continue
		}
		if _, err := os.Lstat(p); err == nil {
			continue // the apply already produced something here
		}
		if err := os.MkdirAll(p, 0o755); err != nil {
			errs = append(errs, fmt.Errorf("recreate directory %s: %w", d, err))
		}
	}

	for _, f := range ev.files {
		if f.stashed == "" {
			continue // never moved
		}
		dst := filepath.Join(repoPath, filepath.FromSlash(f.rel))
		if !parentsSafe(repoPath, f.rel) {
			errs = append(errs, fmt.Errorf("%s not restored: a parent path component is not a plain directory (refusing to write through it); original kept at %s", f.rel, f.stashed))
			continue
		}
		if _, err := os.Lstat(dst); err == nil {
			errs = append(errs, fmt.Errorf("%s not restored: the applied state put content at that path (file/directory conflict); original kept at %s", f.rel, f.stashed))
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			errs = append(errs, fmt.Errorf("%s not restored: create parent: %w; original kept at %s", f.rel, err, f.stashed))
			continue
		}
		if err := os.Rename(f.stashed, dst); err != nil {
			errs = append(errs, fmt.Errorf("%s not restored: %w; original kept at %s", f.rel, err, f.stashed))
		}
	}
	return errs
}

// parentsSafe reports whether every existing component of rel's parent
// path is a plain directory. A symlink (or file) among the parents
// means a restore would follow it out of the intended tree — possibly
// out of the repository entirely — so it is refused.
func parentsSafe(repoPath, rel string) bool {
	dir := path.Dir(path.Clean("/" + rel))
	if dir == "/" {
		return true
	}
	cur := repoPath
	for _, part := range strings.Split(strings.TrimPrefix(dir, "/"), "/") {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			continue // absent components are created as plain directories
		}
		if err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
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
