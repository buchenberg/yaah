package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	croft "github.com/buchenberg/croft"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// workspace_bootstrap.go implements the D1 bootstrap (isolated-workspace plan
// §2.3): the host working tree is copied into the sandbox at session start,
// a baseline state is captured, and the session's outward delta can be
// applied back onto the host when the work is accepted. Copy-in,
// capture/diff during, apply-out on accept — effects stay in the container
// until they are deliberately let out, which is the property that makes an
// isolated attempt safe to discard.

// checkHostGitWorkTree refuses a bootstrap source the copy-in cannot
// reproduce faithfully. The isolated story is git-anchored end to end:
// the containerd backend's Diff runs against the in-container git HEAD,
// and apply-out's delta is a git patch. A non-git source could copy in,
// but its diffs, captures, and apply-out would all be undeliverable —
// better to fail the session at startup than mid-review. Three subtler
// layouts are refused for the same reason, each after the copy-in would
// have already gone wrong:
//
//   - a subdirectory of the work tree: it copies in without the
//     repository, so the in-container HEAD the delta machinery needs
//     would not exist;
//   - a linked worktree or submodule (a .git file, not a directory): the
//     file points at administrative data outside the copied tree, which
//     would not arrive;
//   - a repository with no commits: an unborn HEAD has nothing for
//     Capture to record, so the delta base the closing Apply establishes
//     would never exist.
//
// Every git invocation is context-bound, like the rest of the startup
// path: a wedged host git must fail the startup bound, not hang past it.
func checkHostGitWorkTree(ctx context.Context, dir string) error {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--is-inside-work-tree").Output()
	if err != nil {
		return fmt.Errorf("workspace bootstrap: %s is not a git work tree (git rev-parse: %w) — the isolated workspace's capture, diff, and apply-out are all git-anchored", dir, err)
	}
	if strings.TrimSpace(string(out)) != "true" {
		return fmt.Errorf("workspace bootstrap: %s is not a git work tree — the isolated workspace's capture, diff, and apply-out are all git-anchored", dir)
	}
	// --show-cdup is empty only at the top level of the work tree.
	out, err = exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-cdup").Output()
	if err != nil {
		return fmt.Errorf("workspace bootstrap: %s: git rev-parse --show-cdup: %w", dir, err)
	}
	if cdup := strings.TrimSpace(string(out)); cdup != "" {
		return fmt.Errorf("workspace bootstrap: %s is a subdirectory of its git work tree (--show-cdup %q) — the bootstrap copies the repository itself in, so pass the top level", dir, cdup)
	}
	if fi, serr := os.Lstat(filepath.Join(dir, ".git")); serr != nil || !fi.IsDir() {
		return fmt.Errorf("workspace bootstrap: %s is not a self-contained repository (.git is not a directory — linked worktrees and submodules keep their git data outside the tree) — the isolated workspace cannot reproduce it", dir)
	}
	if herr := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--verify", "--quiet", "HEAD").Run(); herr != nil {
		return fmt.Errorf("workspace bootstrap: %s has no commits — the isolated workspace's capture, diff, and apply-out are anchored to git HEAD, which does not exist yet", dir)
	}
	return nil
}

// gitTrackedFiles returns the repository's tracked paths, slash-separated
// and work-tree-relative. This is the set the copy-in must reproduce
// exactly: a tracked entry the container lacks becomes a deletion in the
// git-anchored delta, which apply-out would then carry onto the host.
func gitTrackedFiles(ctx context.Context, dir string) (map[string]bool, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "ls-files", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("workspace bootstrap: git ls-files: %w", err)
	}
	tracked := make(map[string]bool)
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 {
			tracked[string(p)] = true
		}
	}
	return tracked, nil
}

// hostTreeDirty reports whether the working tree differs from HEAD in a
// way the copy-in reproduces: tracked files changed (staged or not), or
// untracked files that will be copied in. Untracked files the deny policy
// excludes do not count — they are absent on both sides, so the copied
// tree still matches HEAD and no container-side git is needed.
func hostTreeDirty(ctx context.Context, dir string, deny []string) (bool, error) {
	if derr := exec.CommandContext(ctx, "git", "-C", dir, "diff", "--quiet", "HEAD").Run(); derr != nil {
		var xerr *exec.ExitError
		if errors.As(derr, &xerr) {
			return true, nil
		}
		return false, fmt.Errorf("workspace bootstrap: git diff HEAD: %w", derr)
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "ls-files", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return false, fmt.Errorf("workspace bootstrap: git ls-files --others: %w", err)
	}
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 && deniedBy(deny, string(p)) == "" {
			return true, nil
		}
	}
	return false, nil
}

// deniedBy reports the first deny pattern matching the slash-separated,
// workspace-relative path p — the base name and the full relative path
// are both tried, mirroring PathValidator's deny matching, so the copy-in
// enforces the same policy the local workspace does. A malformed pattern
// denies: discarding the error would silently disable the protection the
// operator configured.
func deniedBy(deny []string, p string) string {
	base := path.Base(p)
	for _, pattern := range deny {
		if matched, merr := filepath.Match(pattern, base); merr != nil || matched {
			return pattern
		}
		if matched, _ := filepath.Match(pattern, p); matched {
			return pattern
		}
	}
	return ""
}

// copyTreeIn copies the host working tree into the sandbox root, file by
// file through the kernel's Sandbox.WriteFile. Every copyable regular
// file lands at the same relative path under the root, parent directories
// are created by the backend's WriteFile, and the full mode rides along —
// perm bits and setuid/setgid/sticky, which the kernel preserves.
//
// Three kinds of entries are deliberately excluded, each loudly:
//
//   - Tracked symlinks fail the bootstrap. The Sandbox interface cannot
//     create symlinks, so the container would hold the tree without them
//     while its git HEAD still has them — the delta machinery would
//     record every one as deleted, and an otherwise no-op apply-out would
//     delete them from the host. Untracked symlinks are only skipped with
//     a warning: git does not see them, so no delta ever will.
//   - Files matching the deny policy are not copied in: the copy-in is
//     the only way in, so it is where the policy is enforced — denied
//     files must never become agent-readable inside the container. A
//     tracked denied file fails the bootstrap instead (excluding it would
//     turn it into a host-side deletion at apply-out, exactly like a
//     tracked symlink); an untracked one — the usual case, a local
//     secret — is simply absent on both sides.
//   - Other special files (FIFOs, devices) are skipped with a warning;
//     git cannot track them, so the delta stays faithful.
//
// Empty directories do not arrive either: nothing writes them, and git
// itself does not track them.
//
// Known cost, deliberate for D1's correctness-first landing: one backend
// exec per file. A bundle-based fast path is the anticipated follow-up.
func copyTreeIn(ctx context.Context, sb croft.Sandbox, src, root string, deny []string, tracked map[string]bool) error {
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
		slashRel := filepath.ToSlash(rel)
		switch {
		case d.IsDir():
			return nil
		case d.Type()&fs.ModeSymlink != 0:
			if tracked[slashRel] {
				return fmt.Errorf("%s: a tracked symlink — sandbox backends cannot create symlinks, so the container tree would differ from its git HEAD and apply-out would delete it from the host; remove the symlink (or materialize its target) before starting an isolated session", rel)
			}
			fmt.Fprintf(os.Stderr, "warning: workspace bootstrap: skipping untracked symlink %s — sandbox backends cannot create symlinks\n", rel)
			return nil
		case d.Type().IsRegular():
			if pattern := deniedBy(deny, slashRel); pattern != "" {
				if tracked[slashRel] {
					return fmt.Errorf("%s: matches the workspace deny pattern %q and is tracked by git — the isolated workspace cannot both exclude it and reproduce the repository; untrack it or narrow the pattern", rel, pattern)
				}
				fmt.Fprintf(os.Stderr, "warning: workspace bootstrap: excluding %s from the sandbox (deny pattern %q)\n", rel, pattern)
				return nil
			}
			info, ierr := d.Info()
			if ierr != nil {
				return ierr
			}
			data, readErr := os.ReadFile(p)
			if readErr != nil {
				return readErr
			}
			// The full mode, not Mode().Perm(): the kernel's WriteFile
			// carries setuid/setgid/sticky, and every other mode bit
			// already round-trips.
			if werr := sb.WriteFile(ctx, path.Join(root, slashRel), data, info.Mode()); werr != nil {
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
// sandbox: verify the source, copy the tree in, anchor the in-container
// git HEAD to the copied tree when it differs from it, capture the
// baseline, then Apply that baseline once.
//
// The HEAD anchor keeps apply-out honest on a dirty source. The
// containerd backend records the applied state's git HEAD as its delta
// base, and every later capture is a patch from that HEAD to the current
// tree. Copying a dirty working tree in without touching HEAD would bake
// every pre-existing staged, unstaged, or untracked change into that
// patch — and applying it back onto the very host those changes came
// from fails, because the hunks are already there. So a dirty source gets
// one synthetic in-container commit of exactly the copied tree; a clean
// one already matches HEAD and no container-side git runs at all (which
// is also why the in-memory fake, whose shell has no git, bootstraps
// clean test repositories fine). The commit lives only in the container:
// the host's staged/unstaged split is untouched.
//
// The closing Apply looks redundant — the sandbox already holds this exact
// tree — but it is what anchors the delta machinery: the containerd
// backend records the applied state's git HEAD as its delta base, so every
// later capture emits a binary patch from the session baseline to the
// current tree. Without it, no state this sandbox captures ever carries an
// outward delta, and apply-out has nothing to apply. For snapshot backends
// without a delta base the Apply is an idempotent reset.
func bootstrapSession(ctx context.Context, ss *SessionSandbox, src string, deny []string) error {
	if err := checkHostGitWorkTree(ctx, src); err != nil {
		return err
	}
	tracked, err := gitTrackedFiles(ctx, src)
	if err != nil {
		return err
	}
	if err := copyTreeIn(ctx, ss.sb, src, ss.ws.root, deny, tracked); err != nil {
		return fmt.Errorf("workspace: copy-in: %w", err)
	}
	dirty, err := hostTreeDirty(ctx, src, deny)
	if err != nil {
		return err
	}
	if dirty {
		if cerr := commitContainerBaseline(ctx, ss); cerr != nil {
			return cerr
		}
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

// commitContainerBaseline anchors the in-container git HEAD to the copied
// tree: stage everything and commit it, as a synthetic baseline only the
// container ever sees. Hooks and signing are disabled — this commit is
// bookkeeping, not history, and a pre-commit hook or signing key from the
// copied .git would fail inside the container for no reason the user can
// act on. The identity is fixed for the same reason: no user config needs
// to exist inside the container.
func commitContainerBaseline(ctx context.Context, ss *SessionSandbox) error {
	for _, req := range []croft.ExecRequest{
		{Command: "git", Args: []string{"add", "-A"}, Cwd: ss.ws.root},
		{Command: "git", Args: []string{
			"-c", "user.name=yaah", "-c", "user.email=yaah@localhost",
			"-c", "commit.gpgsign=false",
			"commit", "-q", "--no-verify", "-m", "yaah: isolated-workspace baseline",
		}, Cwd: ss.ws.root},
	} {
		res, err := ss.sb.Exec(ctx, req)
		if err != nil {
			return fmt.Errorf("workspace: baseline commit: git %s: %w", strings.Join(req.Args, " "), err)
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("workspace: baseline commit: git %s: exit %d: %s", strings.Join(req.Args, " "), res.ExitCode, strings.TrimSpace(res.Stderr))
		}
	}
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
func (s *SessionSandbox) Baseline() *croft.WorkspaceState { return s.baseline }

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
	if raw, present := cur.Data[croft.StateKeyDeltaPatch]; present {
		b64, isStr := raw.(string)
		if !isStr {
			// A present-but-undecodable patch must not fall through to
			// the no-op or copy-out branches: that would report success
			// while discarding the session's changes.
			return fmt.Errorf("workspace: apply-out: %s has type %T, want a base64 string — refusing to guess at the backend's delta", croft.StateKeyDeltaPatch, raw)
		}
		patch, derr := base64.StdEncoding.DecodeString(b64)
		if derr != nil {
			return fmt.Errorf("workspace: apply-out: decode delta patch: %w", derr)
		}
		return applyGitPatch(ctx, hostDir, patch)
	}
	if _, ok := cur.Data[croft.StateKeyDeltaBase]; ok {
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
// kernel's Diff against the baseline, and each is written out with the same
// crash safety the host-side tools use. A changed path is classified by
// reading and statting it in the sandbox: readable content is written out
// (its mode taken from the sandbox entry when one is reachable, so a new
// executable arrives executable), a missing path is deleted on the host, a
// directory is ensured to exist — and every other read failure propagates:
// a transport error must fail apply-out, not pass as a directory. This path
// remains blind to mode-only changes.
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
		// The single door stays a single door: a backend reporting a path
		// outside the workspace root is refused before any host operation,
		// not trimmed and trusted to be contained.
		if !filepath.IsLocal(rel) {
			return fmt.Errorf("workspace: apply-out: changed path %q escapes the workspace root — refusing to touch the host", p)
		}
		host := filepath.Join(hostDir, filepath.FromSlash(rel))
		sbPath := path.Join(s.ws.root, rel)
		data, rerr := s.sb.ReadFile(ctx, sbPath)
		switch {
		case rerr == nil:
			perm := fs.FileMode(0o644)
			if info, serr := s.ws.Stat(ctx, sbPath); serr == nil {
				perm = info.Mode().Perm()
			} else if hinfo, herr := os.Lstat(host); herr == nil {
				// No sandbox stat available: fall back to the mode of the
				// host file being replaced.
				perm = hinfo.Mode().Perm()
			}
			if werr := atomicWriteFile(host, data, perm); werr != nil {
				return fmt.Errorf("workspace: apply-out: write %s: %w", rel, werr)
			}
		case errors.Is(rerr, fs.ErrNotExist):
			// The path is gone in the sandbox, whatever it was on the host.
			if rerr := os.RemoveAll(host); rerr != nil {
				return fmt.Errorf("workspace: apply-out: delete %s: %w", rel, rerr)
			}
		default:
			// An unreadable path is a directory only if the sandbox says
			// so; every other read failure is a real error.
			info, serr := s.ws.Stat(ctx, sbPath)
			switch {
			case serr == nil && info.IsDir():
				if merr := os.MkdirAll(host, 0o755); merr != nil {
					return fmt.Errorf("workspace: apply-out: mkdir %s: %w", rel, merr)
				}
			case errors.Is(serr, fs.ErrNotExist):
				if rerr := os.RemoveAll(host); rerr != nil {
					return fmt.Errorf("workspace: apply-out: delete %s: %w", rel, rerr)
				}
			default:
				return fmt.Errorf("workspace: apply-out: read %s: %w", rel, rerr)
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
