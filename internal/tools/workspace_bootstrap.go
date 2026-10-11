package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	containerdbackend "github.com/buchenberg/shepherd-kernel-go/sandbox/containerd"
)

// workspace_bootstrap.go implements the D1 bootstrap (isolated-workspace plan
// §2.3): the host working tree is copied into the sandbox at session start,
// a baseline state is captured, and the session's outward delta can be
// applied back onto the host when the work is accepted. Copy-in,
// capture/diff during, apply-out on accept — effects stay in the container
// until they are deliberately let out, which is the property
// supervised_task's rollback depends on.

// checkHostGitWorkTree refuses a bootstrap source that is not a git work
// tree. The isolated story is git-anchored end to end: the containerd
// backend's Diff runs against the in-container git HEAD, and apply-out's
// delta is a git patch. A non-git source could copy in, but its diffs,
// captures, and apply-out would all be undeliverable — better to fail the
// session at startup than mid-review.
func checkHostGitWorkTree(dir string) error {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("workspace bootstrap: %s is not a git work tree (git rev-parse: %w) — the isolated workspace's capture, diff, and apply-out are all git-anchored", dir, err)
	}
	if strings.TrimSpace(string(out)) != "true" {
		return fmt.Errorf("workspace bootstrap: %s is not a git work tree — the isolated workspace's capture, diff, and apply-out are all git-anchored", dir)
	}
	return nil
}

// copyTreeIn copies the host working tree into the sandbox root, file by
// file through the kernel's Sandbox.WriteFile. Every regular file lands at
// the same relative path under the root, parent directories are created by
// the backend's WriteFile, and the perm bits ride along.
//
// Known costs and gaps, deliberate for D1's correctness-first landing:
//
//   - One backend exec per file. A large working tree is slow to bootstrap;
//     a bundle-based fast path is the anticipated follow-up once D1 is
//     validated in contact with real sessions.
//   - Symlinks are skipped with a warning: the Sandbox interface cannot
//     create them, and silently materializing their targets would change
//     what the repository is.
//   - Empty directories do not arrive: nothing writes them, and git itself
//     does not track them.
func copyTreeIn(ctx context.Context, sb shepherd.Sandbox, src, root string) error {
	src = filepath.Clean(src)
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == src {
			return nil
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		switch {
		case d.IsDir():
			return nil
		case d.Type()&fs.ModeSymlink != 0:
			fmt.Fprintf(os.Stderr, "warning: workspace bootstrap: skipping symlink %s — sandbox backends cannot create symlinks\n", rel)
			return nil
		case d.Type().IsRegular():
			info, ierr := d.Info()
			if ierr != nil {
				return ierr
			}
			data, readErr := os.ReadFile(p)
			if readErr != nil {
				return readErr
			}
			if werr := sb.WriteFile(ctx, path.Join(root, filepath.ToSlash(rel)), data, info.Mode().Perm()); werr != nil {
				return fmt.Errorf("%s: %w", rel, werr)
			}
			return nil
		default:
			fmt.Fprintf(os.Stderr, "warning: workspace bootstrap: skipping special file %s\n", rel)
			return nil
		}
	})
}

// bootstrapSession runs the D1 sequence on a freshly created session
// sandbox: verify the source, copy the tree in, capture the baseline, then
// Apply that baseline once.
//
// The closing Apply looks redundant — the sandbox already holds this exact
// tree — but it is what anchors the delta machinery: the containerd
// backend records the applied state's git HEAD as its delta base, so every
// later capture emits a binary patch from the session baseline to the
// current tree. Without it, no state this sandbox captures ever carries an
// outward delta, and apply-out has nothing to apply. For snapshot backends
// without a delta base the Apply is an idempotent reset.
func bootstrapSession(ctx context.Context, ss *SessionSandbox, src string) error {
	if err := checkHostGitWorkTree(src); err != nil {
		return err
	}
	if err := copyTreeIn(ctx, ss.sb, src, ss.ws.root); err != nil {
		return fmt.Errorf("workspace: copy-in: %w", err)
	}
	base, err := ss.sb.Capture(ctx)
	if err != nil {
		return fmt.Errorf("workspace: capture baseline: %w", err)
	}
	if err := ss.sb.Apply(ctx, base); err != nil {
		return fmt.Errorf("workspace: rebind to baseline: %w", err)
	}
	ss.baseline = &base
	return nil
}

// DiffSinceBaseline reports what changed in the sandbox against the session
// baseline — the "capture/diff during" half of D1. maxLines <= 0 is
// unbounded.
func (s *SessionSandbox) DiffSinceBaseline(ctx context.Context, maxLines int) (string, []string, error) {
	if s.baseline == nil {
		return "", nil, errors.New("workspace: diff: no session baseline — the sandbox was started without a bootstrap")
	}
	return s.sb.Diff(ctx, *s.baseline, maxLines)
}

// Baseline returns the session's baseline state, or nil when the session
// started without a bootstrap.
func (s *SessionSandbox) Baseline() *shepherd.WorkspaceState { return s.baseline }

// ApplyOut applies the session's outward delta onto the host working tree —
// the "apply-out on accept" half of D1. It is the single door changes leave
// the container through: nothing else writes the host repo in isolated
// mode.
//
// The delta artifact is the backend's, not reconstructed here:
//
//   - A git binary patch (containerd: StateKeyDeltaPatch, base64) is applied
//     with `git apply --binary`, which carries binary file changes, refuses
//     a moved host parent loudly instead of guessing, and needs no
//     parse-and-rewrite of content.
//   - A present delta base with no patch is a clean capture: nothing to do.
//   - A backend without delta machinery (the in-memory fake, and any future
//     backend that records plain tree states) falls back to copying the
//     changed paths out through the kernel's ReadFile — content-accurate
//     for files, existence-accurate for deletions, but blind to mode-only
//     changes.
//
// The host tree is trusted not to have moved since copy-in. Applying onto a
// moved parent is settlement's problem (kernel Seal/Settle with
// DeltaApplier), deliberately out of scope here: this verb fails loudly
// rather than merging.
func (s *SessionSandbox) ApplyOut(ctx context.Context, hostDir string) error {
	if s.baseline == nil {
		return errors.New("workspace: apply-out: no session baseline — the sandbox was started without a bootstrap")
	}
	cur, err := s.sb.Capture(ctx)
	if err != nil {
		return fmt.Errorf("workspace: apply-out: capture: %w", err)
	}
	if b64, ok := cur.Data[containerdbackend.StateKeyDeltaPatch].(string); ok {
		patch, derr := base64.StdEncoding.DecodeString(b64)
		if derr != nil {
			return fmt.Errorf("workspace: apply-out: decode delta patch: %w", derr)
		}
		return applyGitPatch(ctx, hostDir, patch)
	}
	if _, ok := cur.Data[containerdbackend.StateKeyDeltaBase]; ok {
		// A clean capture: the backend recorded its delta base and found
		// nothing between it and the current tree.
		return nil
	}
	return s.copyOut(ctx, hostDir)
}

// applyGitPatch applies a git binary patch in the host working tree.
func applyGitPatch(ctx context.Context, hostDir string, patch []byte) error {
	if len(bytes.TrimSpace(patch)) == 0 {
		return nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", hostDir, "apply", "--binary", "--whitespace=nowarn", "-")
	cmd.Stdin = bytes.NewReader(patch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("workspace: apply-out: git apply: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// copyOut is the backend-agnostic fallback: the changed paths come from the
// kernel's Diff against the baseline, and each is classified by reading it
// in the sandbox — readable content is written out, a missing path is
// deleted on the host, and a directory is ensured to exist.
func (s *SessionSandbox) copyOut(ctx context.Context, hostDir string) error {
	_, changed, err := s.sb.Diff(ctx, *s.baseline, 0)
	if err != nil {
		return fmt.Errorf("workspace: apply-out: diff: %w", err)
	}
	for _, p := range changed {
		rel := s.rootRel(p)
		if rel == "" {
			continue
		}
		host := filepath.Join(hostDir, filepath.FromSlash(rel))
		data, rerr := s.sb.ReadFile(ctx, path.Join(s.ws.root, rel))
		switch {
		case rerr == nil:
			perm := fs.FileMode(0o644)
			if info, ierr := os.Lstat(host); ierr == nil {
				perm = info.Mode().Perm()
			}
			if werr := os.WriteFile(host, data, perm); werr != nil {
				return fmt.Errorf("workspace: apply-out: write %s: %w", rel, werr)
			}
		case errors.Is(rerr, fs.ErrNotExist):
			// The path is gone in the sandbox, whatever it was on the host.
			if rerr := os.RemoveAll(host); rerr != nil {
				return fmt.Errorf("workspace: apply-out: delete %s: %w", rel, rerr)
			}
		default:
			// A directory (or other non-file entry) changed: ensure it
			// exists. Mode-only changes are not carried by this path.
			if merr := os.MkdirAll(host, 0o755); merr != nil {
				return fmt.Errorf("workspace: apply-out: mkdir %s: %w", rel, merr)
			}
		}
	}
	return nil
}

// rootRel normalizes a sandbox-side path from a Diff report into a path
// relative to the workspace root: snapshot-style backends report absolute
// in-sandbox paths, git-style backends report workdir-relative ones.
func (s *SessionSandbox) rootRel(p string) string {
	p = strings.TrimPrefix(p, s.ws.root+"/")
	return strings.TrimPrefix(p, "/")
}
