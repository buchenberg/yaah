package tools

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

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/goatherd-runtime/effect"
	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
)

// workspace_bootstrap.go implements the D1 bootstrap (isolated-workspace plan
// §2.3): the host working tree is copied into the sandbox at session start,
// a baseline state is captured, and the session's outward delta can be
// applied back onto the host when the work is accepted. Copy-in,
// capture/diff during, apply-out on accept — effects stay in the container
// until they are deliberately let out, which is the property that makes an
// isolated attempt safe to discard.

// checkHostGitWorkTree refuses a bootstrap source the copy-in cannot reproduce
// faithfully. The copy is file by file, and the container backend cannot create
// symlinks, so git's tracked-file list is what tells the copy-in whether a
// skipped entry would silently change what the repository contains.
//
// The container lane currently bootstraps a repository source: the copy
// includes `.git` so the agent can use git inside the sandbox, and the
// tracked-file list drives the symlink and deny-policy decisions below.
// Broadening it to an arbitrary directory is a separate change; until then,
// two layouts are refused because the copy would be incomplete or the
// copied git metadata would point outside the tree:
//
//   - a subdirectory of the work tree: it copies in without the repository;
//   - a linked worktree or submodule (a .git file, not a directory): the
//     file points at administrative data outside the copied tree.
//
// Every git invocation is context-bound, like the rest of the startup
// path: a wedged host git must fail the startup bound, not hang past it.
func checkHostGitWorkTree(ctx context.Context, dir string) error {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--is-inside-work-tree").Output()
	if err != nil {
		return fmt.Errorf("workspace bootstrap: %s is not a git work tree (git rev-parse: %w) — the isolated workspace currently bootstraps a repository source", dir, err)
	}
	if strings.TrimSpace(string(out)) != "true" {
		return fmt.Errorf("workspace bootstrap: %s is not a git work tree — the isolated workspace currently bootstraps a repository source", dir)
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
	return nil
}

// gitTrackedFiles returns the repository's tracked paths, slash-separated
// and work-tree-relative. The copy-in uses it to distinguish repository
// content from incidental files: a tracked entry the container cannot represent
// (a symlink) or must exclude (a deny pattern) fails the bootstrap instead of
// silently changing what the agent sees.
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
//     create symlinks, so the container would silently hold a tree missing
//     a file the repository tracks. Untracked symlinks are only skipped with
//     a warning: git does not see them, so they are not repository content.
//   - Files matching the deny policy are not copied in: the copy-in is
//     the only way in, so it is where the policy is enforced — denied
//     files must never become agent-readable inside the container. A
//     tracked denied file fails the bootstrap instead (excluding it would
//     silently change what the repository contains); an untracked one — the
//     usual case, a local secret — is simply absent on both sides.
//   - Other special files (FIFOs, devices) are skipped with a warning;
//     git cannot track them, and the copy cannot represent them.
//
// Empty directories do not arrive either: nothing writes them, and git
// itself does not track them.
//
// Known cost, deliberate for D1's correctness-first landing: one WriteFile
// round trip per file. A bundle-based fast path is the anticipated follow-up.
func copyTreeIn(ctx context.Context, sb goatherdsandbox.Sandbox, src, root string, deny []string, tracked map[string]bool) error {
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
				return fmt.Errorf("%s: a tracked symlink — sandbox backends cannot create symlinks, so the container tree would be missing a file the repository tracks; remove the symlink (or materialize its target) before starting an isolated session", rel)
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
// sandbox: verify the source, copy the tree in, then capture the baseline the
// session's diffs and apply-out are measured against.
//
// Nothing is applied at the end. A capture already commits the active layer and
// continues on a fresh layer parented to it, so the workspace is exactly the
// baseline and a diff against that state is the session's outward delta. The
// old closing Apply existed to anchor a git-HEAD delta base for the backend's
// delta machinery; croft states carry their own snapshot lineage now, and this
// path never calls ApplyDelta, so the closing rebind only stopped and restarted
// the task for a field nothing read. It is removed.
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
	base, err := ss.sb.Capture(ctx)
	if err != nil {
		return fmt.Errorf("workspace: capture baseline: %w", err)
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
func (s *SessionSandbox) Baseline() *shepherdtrace.WorkspaceState { return s.baseline }

// effectChanger is the optional backend capability that yields typed workspace
// changes in the runtime's neutral vocabulary. The containerd adapter and the
// in-memory fake provide it; the git backend does not, so callers must tolerate
// its absence rather than treat it as an error.
//
// Declared locally and structurally rather than imported from the adapter, so
// this package does not couple to a backend: any sandbox offering the method
// satisfies it, and a backend that does not simply fails the assertion.
type effectChanger interface {
	EffectChanges(ctx context.Context, ws shepherdtrace.WorkspaceState) ([]effect.Change, error)
}

// SupportsEffects reports whether the session's backend can supply typed
// workspace effects at all. Callers check this before EffectChanges so that a
// backend without the capability is a silent no-op rather than an error — the
// git lane has no typed change set to give.
func (s *SessionSandbox) SupportsEffects() bool {
	_, ok := s.sb.(effectChanger)
	return ok
}

// EffectChanges reports what the session changed in the workspace, against the
// session baseline, in the runtime's neutral effect vocabulary.
//
// The baseline is the same anchor DiffSinceBaseline and ApplyOut use, so the
// effects describe exactly the delta the accept seam would land — not the whole
// tree, and not whatever happened to change since the last call.
//
// Read-only: unlike croft's Capture this commits no snapshot and pins no lease,
// so it costs nothing durable and is safe to call after ApplyOut has already
// written the host.
//
// It is not free, though. This is a third backend round-trip on the accept path,
// after the tool's DiffSinceBaseline and the diff copyOut runs to build its work
// list — and that an accept already diffed twice is a pre-existing cost, not one
// this adds. Each call is read-only against the daemon, but on a large tree the
// latency is measurable. Collapsing them means threading a single change set out
// of copyOut for the tool to reuse, which changes ApplyOut's contract; worth
// doing if accept latency ever shows up in practice, not before.
func (s *SessionSandbox) EffectChanges(ctx context.Context) ([]effect.Change, error) {
	if s.baseline == nil {
		return nil, errors.New("workspace: effects: no session baseline — the sandbox was started without a bootstrap")
	}
	ec, ok := s.sb.(effectChanger)
	if !ok {
		return nil, fmt.Errorf("workspace: effects: backend %q does not report typed workspace changes", s.sb.Backend())
	}
	changes, err := ec.EffectChanges(ctx, *s.baseline)
	if err != nil {
		return nil, fmt.Errorf("workspace: effects: %w", err)
	}
	return changes, nil
}

// filterEffectChanges removes the paths that the accept seam and proposal
// seal never copy: bookkeeping trees and workspace deny matches. Effect
// recording must use the same filter, or a denied path would be reported as
// landed even though copyOut skipped it.
func (s *SessionSandbox) filterEffectChanges(changes []effect.Change) []effect.Change {
	out := make([]effect.Change, 0, len(changes))
	for _, c := range changes {
		rel := s.rootRel(c.Path)
		if rel == "" || c.IsDir || excludedFromManifest(rel) || deniedBy(s.deny, rel) != "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// ApplyOut applies the session's outward delta onto the host working tree —
// the "apply-out on accept" half of D1. It is the single door changes leave
// the container through: nothing else writes the host repo in isolated
// mode.
//
// The changed paths come from the Sandbox Diff against the session baseline,
// and each is written out through ReadFile with the same crash safety the
// host-side tools use. This is backend-agnostic: it is what the in-memory fake
// has always exercised, and it is what the containerd backend uses now that
// croft records plain snapshot states.
//
// Previously this verb preferred a git binary patch carried in the captured
// state (croft's StateKeyDeltaPatch), falling back to the copy-out path only
// for backends without delta machinery. goatherd-croft replaced that git-based
// state model with containerd snapshot keys in v0.7.0, so no backend produces
// such a patch any more and the branch was unreachable. It is removed rather
// than left to rot; see yaah bead yaah-vlwm.
//
// Removing it also removes this verb's Capture. That is a resource win, not
// just a simplification: croft's Capture commits the active layer and pins it
// under a lease, so capturing here purely to read a patch left a pinned
// snapshot behind on every accept, with nothing to release it. Diff is
// read-only and commits nothing.
//
// Mode fidelity: a `chmod` made inside the session does survive accept, but
// only because copyOut chmods after writing — see the comment there. It is
// worth recording why, because the obvious diagnosis is wrong. A mode-only
// change *does* appear in Diff's path list: verified against a real containerd
// daemon, croft's Changes classifies it as a modify carrying the new mode, and
// the adapter derives Diff's file list from Changes. The loss happened one step
// later, in atomicWriteFile, which preserves an existing file's mode by design.
// So the fix belongs at the write, not in how the work list is built.
//
// Still narrowed, and tracked as yaah-vlwm: perm is taken with .Perm(), so
// setuid/setgid/sticky bits do not cross back to the host even though the
// sandbox's Stat reconstructs them and copy-in carries them in. Whether they
// should is a policy question about propagating privilege bits from a weaker
// boundary to a stronger one, not a bug to fix silently.
//
// The host tree is trusted not to have moved since copy-in. Applying onto a
// moved parent is settlement's problem (kernel Seal/Settle with
// DeltaApplier), deliberately out of scope here: this verb fails loudly
// rather than merging.
func (s *SessionSandbox) ApplyOut(ctx context.Context, hostDir string) error {
	if s.baseline == nil {
		return errors.New("workspace: apply-out: no session baseline — the sandbox was started without a bootstrap")
	}
	return s.copyOut(ctx, hostDir)
}

// copyOut applies the session's changes to the host tree: the changed paths
// come from the Sandbox Diff against the baseline, and each is written out with
// the same crash safety the host-side tools use. A changed path is classified
// by reading and statting it in the sandbox: readable content is written out
// (its mode taken from the sandbox entry when one is reachable, so a new
// executable arrives executable), a missing path is deleted on the host, a
// directory is ensured to exist — and every other read failure propagates:
// a transport error must fail apply-out, not pass as a directory.
//
// This is backend-agnostic, which is why it is now the only apply-out path
// rather than a fallback: it is what the in-memory fake has always exercised,
// and it serves the containerd backend now that croft records plain snapshot
// states instead of a git binary patch.
//
// It is not blind to mode-only changes, contrary to what this comment used to
// claim. A chmod with unchanged content does appear in Diff's path list —
// croft's Changes reports it as a modify carrying the new mode, verified
// against a real daemon — and the explicit chmod below makes it land. What
// still does not cross is setuid/setgid/sticky, because perm is masked with
// .Perm(); that is a deliberate open question, not an oversight (yaah-vlwm).
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
		if excludedFromManifest(rel) || deniedBy(s.deny, rel) != "" {
			// The accept seam applies the same bookkeeping and deny filters as
			// a proposal seal; neither may copy .git, .yaah, or a denied path
			// onto the host.
			continue
		}
		host := filepath.Join(hostDir, filepath.FromSlash(rel))
		sbPath := path.Join(s.ws.root, rel)
		data, rerr := s.sb.ReadFile(ctx, sbPath)
		switch {
		case rerr == nil:
			// perm is authoritative only when it came from the sandbox, which is
			// the only place that knows what the session's file should look
			// like. The 0o644 default is what atomicWriteFile applies to a
			// genuinely new file; for a path that already exists on the host it
			// re-derives and preserves that file's own mode, so the default is
			// never imposed over an existing one.
			perm := fs.FileMode(0o644)
			modeFromSandbox := false
			if info, serr := s.ws.Stat(ctx, sbPath); serr == nil {
				perm = info.Mode().Perm()
				modeFromSandbox = true
			}
			// There is deliberately no "fall back to the host file's mode"
			// branch here, though one used to exist. It read os.Lstat(host),
			// and Lstat on a symlink reports the *link's* mode — 0777 on POSIX
			// — while atomicWriteFile replaces that path with a regular file.
			// For a dangling symlink atomicWriteFile's own os.Stat fails, so it
			// keeps the caller's perm and 0777 lands on a regular file:
			// world-writable and world-executable on the host. That was true
			// before this verb grew a chmod, and gating the chmod below only
			// closed half of it.
			//
			// Dropping the fallback costs nothing. A host path that exists as a
			// regular file gets its mode preserved by atomicWriteFile's own
			// Stat regardless of what is passed in; a path that does not exist
			// makes Lstat fail too, leaving the same 0o644. The only case where
			// the branch ever changed the outcome was a symlink, which is
			// exactly the case that was unsafe.
			if werr := atomicWriteFile(host, data, perm); werr != nil {
				return fmt.Errorf("workspace: apply-out: write %s: %w", rel, werr)
			}
			// atomicWriteFile preserves an existing file's mode by design —
			// correct for a host-side tool editing a file, where changing the
			// content must not change the mode. Copy-out is the opposite case:
			// the sandbox is the authority, because the host file is being
			// replaced by the session's version of it. Without this chmod a
			// `chmod +x` made inside the container is silently dropped on
			// accept, since the path already exists on the host from the
			// copy-in. That is the mode-fidelity loss recorded as yaah-vlwm.
			//
			// The diff is not where it is lost: verified against a real
			// containerd daemon, a mode-only change does appear in Diff's path
			// list (croft's Changes reports it as a modify carrying the new
			// mode), so the path reaches this branch and only the write
			// discarded the mode.
			//
			// Gated on modeFromSandbox, and the gate is a security boundary
			// rather than tidiness: chmodding a mode we did not get from the
			// sandbox would impose a guess on a host file. A 0o644 guess over a
			// host file the user had at 0o600 would loosen it to
			// world-readable, which is the same class of mistake as the 0777
			// case above in the other direction. When the sandbox cannot tell
			// us the mode, we do not know it, so we impose nothing and let the
			// write's own preservation behaviour stand.
			if modeFromSandbox {
				if cerr := os.Chmod(host, perm); cerr != nil {
					return fmt.Errorf("workspace: apply-out: chmod %s: %w", rel, cerr)
				}
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
