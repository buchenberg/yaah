package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherd "github.com/buchenberg/goatherd-runtime"
	"github.com/buchenberg/goatherd-runtime/effect"
	"github.com/buchenberg/yaah/internal/agent/subagent"
)

// git_delta.go implements the cross-backend settlement artifact for a
// container proposal (docs/shepherd-parity.md §Container settlement: GitDelta,
// decision D11). The parent world is a git sandbox and the dispatch world is a
// container, so there is no shared state language to compare or merge. The
// artifact is a change manifest of whole-path post-images, produced from the
// dispatch container's snapshot change set (Changes/ReadFile), never from git
// plumbing: the container does not have to be a repository, and the manifest is
// the same snapshot change set apply-out consumes.
//
// Nothing here parses or applies a text patch: a manifest is exact, and
// applying it is a writer rather than a patch parser. Whole-path replacement
// is deliberate — no 3-way merge, no hunk merge, no content synthesis.

const (
	// gitDeltaMaxContentBytes caps the total post-image content a proposal may
	// carry (32 MiB). Exceeding it fails the seal with a clear message; the
	// manifest is never truncated, because a truncated post-image would apply
	// silently corrupted content.
	gitDeltaMaxContentBytes = 32 << 20

	// gitDeltaMaxFiles caps the file count for the same reason. A manifest
	// large enough to hit this is not reviewable anyway.
	gitDeltaMaxFiles = 10000

	// gitDeltaMaxReviewBytes bounds the display-only unified diff persisted
	// beside the manifest. Settlement never reads it; it exists so `show` can
	// render git's own review after the container is gone.
	gitDeltaMaxReviewBytes = 4 << 20
)

// Delta change kinds. These are the only kinds a manifest may carry; a
// typechange (regular file ↔ symlink/submodule) is refused at seal because
// the manifest cannot represent it faithfully.
const (
	deltaKindAdd    = "add"
	deltaKindModify = "modify"
	deltaKindDelete = "delete"
)

// GitDelta is a container proposal's settlement artifact: the baseline state's
// revision it was measured against, and one whole-path post-image per changed
// path.
//
// The name is historical: the artifact was once produced by git plumbing inside
// the container. It is now the snapshot differ's change set, and the JSON field
// is basis_revision; nothing about producing or settling it requires a
// repository.
type GitDelta struct {
	// BasisRevision is the container baseline's state revision (croft's snapshot
	// key). The manifest's changes are the difference between that state and the
	// workspace at seal time.
	BasisRevision string        `json:"basis_revision"`
	Changes       []DeltaChange `json:"changes"`
}

// DeltaChange is one changed path in a GitDelta. Content is the post-image and
// is empty for a delete.
type DeltaChange struct {
	// Path is slash-separated and repo-relative.
	Path string `json:"path"`
	// Kind is "add", "modify", or "delete".
	Kind string `json:"kind"`
	// Mode is the post-image permission bits (the pre-image mode for a delete;
	// it is ignored on apply). The manifest carries regular files only.
	Mode fs.FileMode `json:"mode"`
	// Content is the post-image bytes; empty for a delete.
	Content []byte `json:"content,omitempty"`
}

// Paths returns the manifest's changed paths, sorted and deduplicated. The
// producer emits them sorted, but a manifest read from disk or built by a
// caller is not assumed to be.
func (d *GitDelta) Paths() []string {
	if d == nil || len(d.Changes) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(d.Changes))
	paths := make([]string, 0, len(d.Changes))
	for _, c := range d.Changes {
		if seen[c.Path] {
			continue
		}
		seen[c.Path] = true
		paths = append(paths, c.Path)
	}
	sort.Strings(paths)
	return paths
}

// RenderDiff renders the manifest as a per-file review. It is the fallback when
// the seal-time unified diff was not persisted (or is unavailable). The review
// is honest about what it is: the post-image content, not a reconstruction of
// the pre-image.
func (d *GitDelta) RenderDiff() string {
	if d == nil || len(d.Changes) == 0 {
		return ""
	}
	var b strings.Builder
	for i, c := range d.Changes {
		if i > 0 {
			b.WriteByte('\n')
		}
		switch c.Kind {
		case deltaKindAdd:
			fmt.Fprintf(&b, "--- /dev/null\n+++ b/%s\n", c.Path)
		case deltaKindDelete:
			fmt.Fprintf(&b, "--- a/%s\n+++ /dev/null\n(deleted)\n", c.Path)
			continue
		default:
			fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", c.Path, c.Path)
		}
		if isBinaryContent(c.Content) {
			fmt.Fprintf(&b, "Binary file %s (%d bytes) — content is in the manifest, not in this review\n", c.Path, len(c.Content))
			continue
		}
		b.WriteString(renderContent(c.Content))
	}
	return b.String()
}

// renderContent emits content as review lines. A post-image has no base to
// compare against, so add and modify both render their lines with a leading '+'
// marker: the reader sees exactly what will land.
func renderContent(content []byte) string {
	if len(content) == 0 {
		return ""
	}
	s := string(content)
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	var b strings.Builder
	for _, line := range lines {
		b.WriteByte('+')
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// isBinaryContent reports whether content is not reviewable as text. The check
// is conservative — invalid UTF-8 or a NUL byte is binary.
func isBinaryContent(content []byte) bool {
	if len(content) == 0 {
		return false
	}
	if bytesIndexByte(content, 0) >= 0 {
		return true
	}
	return !utf8.Valid(content)
}

func bytesIndexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// safeDeltaPath validates and normalizes a manifest path. The result is the
// cleaned slash-separated relative path. Absolute paths, traversal, NUL,
// backslashes, Windows volume paths, and anything under `.git` are refused.
func safeDeltaPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("git delta: empty path")
	}
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("git delta: path %q contains a NUL byte", p)
	}
	if strings.Contains(p, `\`) {
		return "", fmt.Errorf("git delta: path %q contains a backslash; manifest paths are slash-separated", p)
	}
	if strings.HasPrefix(p, "/") || drivePath(p) {
		return "", fmt.Errorf("git delta: path %q is absolute", p)
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("git delta: path %q escapes the repository root", p)
	}
	if !filepath.IsLocal(filepath.FromSlash(clean)) {
		return "", fmt.Errorf("git delta: path %q is not a local relative path", p)
	}
	for _, comp := range strings.Split(clean, "/") {
		if strings.EqualFold(comp, ".git") {
			return "", fmt.Errorf("git delta: path %q touches git metadata", p)
		}
	}
	return clean, nil
}

// pathKey is the comparability key for a manifest path on this host. Windows
// and macOS volume defaults fold case, so two spellings that the host would
// treat as one file must compare equal — a path-disjoint check that missed
// that would let apply overwrite the host's own change.
func pathKey(p string) string {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.ToLower(p)
	}
	return p
}

// validateDelta checks a manifest without touching the host: every path safe,
// every kind known, the content and file caps respected. It is shared by the
// producer's caller and the host writer so both refuse the same manifests.
func validateDelta(d *GitDelta) error {
	if d == nil {
		return errors.New("git delta: no manifest")
	}
	if len(d.Changes) > gitDeltaMaxFiles {
		return fmt.Errorf("git delta: %d changed files exceeds the %d-file cap", len(d.Changes), gitDeltaMaxFiles)
	}
	seen := make(map[string]bool, len(d.Changes))
	total := 0
	for _, c := range d.Changes {
		p, err := safeDeltaPath(c.Path)
		if err != nil {
			return err
		}
		if p != c.Path {
			return fmt.Errorf("git delta: path %q is not normalized (want %q)", c.Path, p)
		}
		if key := pathKey(p); seen[key] {
			return fmt.Errorf("git delta: duplicate path %q on this host's filesystem", p)
		} else {
			seen[key] = true
		}
		switch c.Kind {
		case deltaKindAdd, deltaKindModify:
			// The manifest writes regular files only: whole-path post-images
			// cannot reproduce a symlink target or a device. Any type bit is
			// refused, including the symlink bit a change set may carry.
			if c.Mode&fs.ModeType != 0 {
				return fmt.Errorf("git delta: %s: mode %04o is not a regular-file mode", c.Path, c.Mode)
			}
		case deltaKindDelete:
			if len(c.Content) != 0 {
				return fmt.Errorf("git delta: %s: a delete carries no content", c.Path)
			}
		default:
			return fmt.Errorf("git delta: %s: unknown change kind %q", c.Path, c.Kind)
		}
		total += len(c.Content)
		if total > gitDeltaMaxContentBytes {
			return fmt.Errorf("git delta: content exceeds the %d-byte cap (refusing to truncate)", gitDeltaMaxContentBytes)
		}
	}
	return nil
}

// captureContainerDelta produces the settlement manifest for a finished body.
//
// It asks the backend for the typed change set between the container's baseline
// state and its current workspace — the same change set apply-out consumes —
// then reads each added or modified file's post-image through ReadFile. No git
// is involved: the container does not have to be a repository, and the manifest
// is the snapshot differ's answer rather than a second opinion computed by
// staging the tree.
//
// Two kinds of path are left out of the manifest:
//
//   - directories, which carry no post-image (git never tracked them either);
//   - repository/proposal bookkeeping: anything under a `.git` directory, and
//     yaah's own `.yaah/proposals` and `.yaah/proposal-releases` trees. The
//     bootstrap copies the repository's git metadata into the container so the
//     agent can use git, and any git command then changes index/log/object
//     files. Those are bookkeeping, not workspace content, and settling them
//     would either fail the path guard or carry megabytes of objects to the
//     host. The proposal trees are excluded for the same reason the host
//     excludes them from git: a proposal must not settle its own registry.
//
// Everything else the change set reports is included, including files a
// `.gitignore` would hide. The seal and apply-out both drop the same
// bookkeeping and deny-policy paths, so proposals and accepts agree on what
// "the workspace changed" means. The content cap still bounds the manifest.
//
// The review is the snapshot diff against the same baseline, capped for
// display. It is not the settlement artifact: the manifest is.
func captureContainerDelta(ctx context.Context, ss *SessionSandbox) (*GitDelta, string, error) {
	baseline := ss.Baseline()
	if baseline == nil {
		return nil, "", errors.New("git delta: the container has no baseline state to measure against")
	}
	changes, err := ss.EffectChanges(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("git delta: read the container change set: %w", err)
	}
	delta := &GitDelta{BasisRevision: baseline.Revision, Changes: make([]DeltaChange, 0, len(changes))}
	total := 0
	filtered := false
	for _, c := range changes {
		// Validate before filtering: a malformed backend path must fail the
		// seal even when it happens to look like bookkeeping or a denied path.
		rel, err := manifestPath(ss, c.Path)
		if err != nil {
			return nil, "", err
		}
		if !filepath.IsLocal(filepath.FromSlash(rel)) {
			return nil, "", fmt.Errorf("git delta: change path %q escapes the workspace root", c.Path)
		}
		if c.IsDir || excludedFromManifest(rel) || deniedBy(ss.deny, rel) != "" {
			filtered = true
			continue
		}
		if len(delta.Changes) >= gitDeltaMaxFiles {
			return nil, "", fmt.Errorf("git delta: changed files exceed the %d-file cap", gitDeltaMaxFiles)
		}
		change, err := manifestChange(ctx, ss, c, gitDeltaMaxContentBytes-total)
		if err != nil {
			return nil, "", err
		}
		total += len(change.Content)
		if total > gitDeltaMaxContentBytes {
			return nil, "", fmt.Errorf("git delta: post-image content exceeds the %d-byte cap; refusing to truncate", gitDeltaMaxContentBytes)
		}
		delta.Changes = append(delta.Changes, change)
	}
	sort.Slice(delta.Changes, func(i, j int) bool { return delta.Changes[i].Path < delta.Changes[j].Path })
	if err := validateDelta(delta); err != nil {
		return nil, "", err
	}
	review := delta.RenderDiff()
	if !filtered {
		// The snapshot diff is the useful review: it shows what changed, not
		// just the post-image. It is display-only and can mention bookkeeping
		// or denied paths, so fall back to the filtered manifest view when any
		// such path was present (the manifest remains the settlement artifact).
		if diff, _, diffErr := ss.DiffSinceBaseline(ctx, 0); diffErr == nil {
			review = diff
		}
	}
	return delta, clipDeltaReview(review), nil
}

// manifestChange turns one typed change into a whole-path manifest change.
//
// Every non-regular add or modify is refused before the post-image read: the
// manifest writes regular files only, so recording a symlink or a special file
// would either silently replace a link with its target text or block on a FIFO.
// A deleted symlink is representable as a plain delete and stays allowed.
func manifestChange(ctx context.Context, ss *SessionSandbox, c effect.Change, remaining int) (DeltaChange, error) {
	rel, err := manifestPath(ss, c.Path)
	if err != nil {
		return DeltaChange{}, err
	}
	p, err := safeDeltaPath(rel)
	if err != nil {
		return DeltaChange{}, err
	}
	switch c.Kind {
	case effect.KindDeleted:
		return DeltaChange{Path: p, Kind: deltaKindDelete}, nil
	case effect.KindCreated, effect.KindPatched:
	default:
		return DeltaChange{}, fmt.Errorf("git delta: %s: unknown change kind %q", c.Path, c.Kind)
	}
	if c.Mode&fs.ModeSymlink != 0 {
		return DeltaChange{}, fmt.Errorf("git delta: %s: a symlink cannot be represented as a whole-path file post-image", c.Path)
	}
	if c.Mode&fs.ModeType != 0 {
		return DeltaChange{}, fmt.Errorf("git delta: %s: mode %04o is not a regular-file post-image", c.Path, c.Mode)
	}
	if c.Size > int64(remaining) {
		return DeltaChange{}, fmt.Errorf("git delta: post-image content exceeds the %d-byte cap; refusing to truncate", gitDeltaMaxContentBytes)
	}
	// Read the path that was validated, not the backend's raw spelling: a
	// backend may report an absolute path or a relative one, but both resolve
	// to the same workspace-relative p after the guard above.
	data, err := ss.sb.ReadFile(ctx, path.Join(ss.ws.root, p))
	if err != nil {
		return DeltaChange{}, fmt.Errorf("git delta: %s: read post-image: %w", c.Path, err)
	}
	if int64(len(data)) != c.Size {
		return DeltaChange{}, fmt.Errorf("git delta: %s: read %d bytes, change set reported %d", c.Path, len(data), c.Size)
	}
	kind := deltaKindModify
	if c.Kind == effect.KindCreated {
		kind = deltaKindAdd
	}
	return DeltaChange{Path: p, Kind: kind, Mode: c.Mode.Perm(), Content: data}, nil
}

// manifestPath normalizes a change-set path to a workspace-relative one and
// refuses an absolute path outside the workspace root. A backend reporting such
// a path is misbehaving; trimming it to a relative path would silently settle a
// file from elsewhere in the container.
func manifestPath(ss *SessionSandbox, p string) (string, error) {
	if strings.HasPrefix(p, "/") && ss.ws.root != "/" {
		prefix := strings.TrimSuffix(ss.ws.root, "/") + "/"
		if !strings.HasPrefix(p, prefix) {
			return "", fmt.Errorf("git delta: change path %q is outside the workspace root %q", p, ss.ws.root)
		}
	}
	return ss.rootRel(p), nil
}

// excludedFromManifest reports whether a workspace-relative path is
// bookkeeping rather than proposable content: git metadata (copied in for the
// agent's use) or yaah's own state tree (excluded from host git for the same
// reason). The whole .yaah tree is excluded, not only the proposal registry:
// worktrees, scratch files, and future state are bookkeeping, not proposal
// content.
func excludedFromManifest(p string) bool {
	for _, comp := range strings.Split(p, "/") {
		if strings.EqualFold(comp, ".git") {
			return true
		}
	}
	return p == ".yaah" || strings.HasPrefix(p, ".yaah/")
}

// clipDeltaReview bounds the display-only diff. The manifest is never clipped.
func clipDeltaReview(review string) string {
	review = strings.TrimRight(review, "\n")
	if len(review) <= gitDeltaMaxReviewBytes {
		return review
	}
	cut := gitDeltaMaxReviewBytes
	for cut > 0 && !utf8.RuneStart(review[cut]) {
		cut--
	}
	return review[:cut] + "\n...[review truncated; the manifest is complete]"
}

// proposalArtifactDir is where a container proposal's manifest lives. It is
// repo-local so a crash leaves the proposal durable without keeping the
// container alive.
func proposalArtifactDir(repoPath, id string) string {
	return filepath.Join(repoPath, ".yaah", "proposals", sanitizeRetainedID(id))
}

// proposalReleaseRoot is where `release` exports a manifest before the
// proposal is consumed.
func proposalReleaseRoot(repoPath string) string {
	return filepath.Join(repoPath, ".yaah", "proposal-releases")
}

// ensureProposalPathsExcluded hides yaah's proposal bookkeeping from the target
// repo's git status. This is not cosmetic: the select precondition re-captures
// the host tree, and the git backend's capture stages untracked files. Without
// the exclusion, writing the manifest would itself move the host basis and make
// every select fail. It must therefore run before the dispatch basis is
// captured.
func ensureProposalPathsExcluded(ctx context.Context, repoPath string) error {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return err
	}
	for _, rel := range []string{".yaah/proposals", ".yaah/proposal-releases"} {
		if err := appendGitInfoExclude(ctx, abs, rel); err != nil {
			return err
		}
	}
	return nil
}

// persistGitDelta writes the manifest durably under the repository. Failure
// fails the seal: a proposal the orchestrator cannot settle is worse than a
// failed dispatch.
// proposalBasis is the host workspace state a container proposal was measured
// against. Persisting it is what lets select re-check the premise after a
// restart: the in-memory proposal cannot carry it across processes.
type proposalBasis struct {
	State  shepherdtrace.WorkspaceState `json:"state"`
	Digest string                       `json:"digest"`
}

// proposalMeta is the small, restart-surviving record beside the manifest. The
// id is stored because the directory name is sanitized; the parent scope keeps
// one session's proposals out of another's registry.
type proposalMeta struct {
	ID             string `json:"id"`
	SessionID      string `json:"session_id"`
	ParentScopeID  string `json:"parent_scope_id"`
	World          string `json:"world"`
	Target         string `json:"target"`
	Result         string `json:"result,omitempty"`
	RunErr         string `json:"run_err,omitempty"`
	ManifestDigest string `json:"manifest_digest"`
}

const (
	proposalDeltaFile  = "delta.json"
	proposalBasisFile  = "basis.json"
	proposalMetaFile   = "meta.json"
	proposalReviewFile = "review.diff"
	proposalApplying   = "applying.json"
	proposalSettled    = "settled.json"
)

// Runtime-level proposal facts (not kernel ABI): the manifest is a world
// artifact, but its seal and settlement must still be legible in the causal
// record. The digest in the fact names exactly which bytes were persisted.
const (
	SchemaProposalSealed  = "yaah.proposal.sealed.v1"
	SchemaProposalSettled = "yaah.proposal.settled.v1"
)

// persistGitDelta writes the manifest and everything settlement needs to
// survive a restart: the host basis and a small metadata record beside it. The
// manifest digest is returned so the seal fact and later verification can name
// exactly what was persisted. Failure fails the seal: a proposal the
// orchestrator cannot settle is worse than a failed dispatch.
func persistGitDelta(
	ctx context.Context,
	repoPath, id string,
	delta *GitDelta,
	review, result, runErr, sessionID, parentScopeID, target string,
	basis shepherdtrace.WorkspaceState,
	basisDigest string,
) (dir, digest string, err error) {
	if err := validateDelta(delta); err != nil {
		return "", "", err
	}
	if basisDigest == "" {
		return "", "", errors.New("persist delta: the host basis digest is required")
	}
	computed, derr := basis.Digest()
	if derr != nil {
		return "", "", fmt.Errorf("persist delta: digest host basis: %w", derr)
	}
	if computed != basisDigest {
		return "", "", fmt.Errorf("persist delta: host basis digest %s does not match the state (%s)", basisDigest, computed)
	}
	if err := ensureProposalPathsExcluded(ctx, repoPath); err != nil {
		return "", "", fmt.Errorf("exclude proposal path from git: %w", err)
	}
	dir = proposalArtifactDir(repoPath, id)
	// The directory name is a sanitized id; two raw ids must never share one
	// artifact directory and silently overwrite each other's metadata.
	if existing, rerr := os.ReadFile(filepath.Join(dir, proposalMetaFile)); rerr == nil {
		var meta proposalMeta
		if json.Unmarshal(existing, &meta) == nil && meta.ID != "" && meta.ID != id {
			return "", "", fmt.Errorf("proposal artifact directory %s already belongs to %s", dir, meta.ID)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("create proposal dir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(delta, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("marshal delta: %w", err)
	}
	sum := sha256.Sum256(data)
	digest = fmt.Sprintf("%x", sum)
	basisData, err := json.MarshalIndent(proposalBasis{State: basis, Digest: basisDigest}, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("marshal basis: %w", err)
	}
	metaData, err := json.MarshalIndent(proposalMeta{
		ID:             id,
		SessionID:      sessionID,
		ParentScopeID:  parentScopeID,
		World:          "container",
		Target:         target,
		Result:         result,
		RunErr:         runErr,
		ManifestDigest: digest,
	}, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("marshal proposal meta: %w", err)
	}

	written := []struct {
		name string
		data []byte
	}{
		{proposalDeltaFile, data},
		{proposalBasisFile, basisData},
		{proposalMetaFile, metaData},
	}
	if review != "" {
		written = append(written, struct {
			name string
			data []byte
		}{proposalReviewFile, []byte(review)})
	}
	for _, f := range written {
		p := filepath.Join(dir, f.name)
		if err := atomicWriteFile(p, f.data, 0o600); err != nil {
			return "", "", fmt.Errorf("write %s: %w", p, err)
		}
	}
	// A crash after the seal must leave a readable manifest, so sync the files
	// and (best-effort) the directory. Directory fsync is not portable; its
	// failure cannot make the manifest unreadable, so it is not fatal.
	for _, f := range written {
		if err := syncFile(filepath.Join(dir, f.name)); err != nil {
			return "", "", fmt.Errorf("sync %s: %w", filepath.Join(dir, f.name), err)
		}
	}
	_ = syncDir(dir)
	return dir, digest, nil
}

// syncFile flushes one file's contents to stable storage. The handle is opened
// read-write because Windows' FlushFileBuffers requires GENERIC_WRITE; a
// read-only handle fails with "Access is denied".
func syncFile(p string) error {
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// syncDir flushes a directory entry (best-effort: not every platform allows
// opening a directory for sync).
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// exportGitDelta copies a released proposal's artifact directory to a retention
// path. The proposal directory itself is removed when the release consumes it,
// so the copy is what makes the release honest.
func exportGitDelta(ctx context.Context, p *retainedProposal) (string, error) {
	if p.artifactDir == "" {
		return "", errors.New("no proposal artifact directory to export")
	}
	if err := ensureProposalPathsExcluded(ctx, p.repoPath); err != nil {
		return "", fmt.Errorf("exclude release path from git: %w", err)
	}
	dst := filepath.Join(proposalReleaseRoot(p.repoPath), sanitizeRetainedID(p.id))
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return "", err
	}
	for _, name := range []string{proposalDeltaFile, proposalBasisFile, proposalMetaFile, proposalReviewFile} {
		src := filepath.Join(p.artifactDir, name)
		data, err := os.ReadFile(src)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if err := atomicWriteFile(filepath.Join(dst, name), data, 0o600); err != nil {
			return "", err
		}
	}
	return dst, nil
}

// deltaStep is one validated host write. Validation of the whole manifest
// happens before any step runs, so a bad path cannot leave a partial apply.
type deltaStep struct {
	abs    string
	change DeltaChange
}

// planGitDelta validates a manifest against a host repository root and resolves
// every destination. It does not touch the filesystem.
func planGitDelta(repoPath string, delta *GitDelta) ([]deltaStep, error) {
	if err := validateDelta(delta); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, err
	}
	steps := make([]deltaStep, 0, len(delta.Changes))
	for _, c := range delta.Changes {
		p, err := safeDeltaPath(c.Path)
		if err != nil {
			return nil, err
		}
		abs := filepath.Join(root, filepath.FromSlash(p))
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return nil, fmt.Errorf("git delta: resolve %q: %w", c.Path, err)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) || !filepath.IsLocal(rel) {
			return nil, fmt.Errorf("git delta: path %q resolves outside the repository root", c.Path)
		}
		steps = append(steps, deltaStep{abs: abs, change: c})
	}
	return steps, nil
}

// applyGitDelta writes a manifest onto the host tree. Whole-path replacement and
// deletion only; the git index is never touched (the orchestrator commits, as
// with every other settlement).
func applyGitDelta(_ context.Context, repoPath string, delta *GitDelta) error {
	steps, err := planGitDelta(repoPath, delta)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(repoPath)
	if err != nil {
		return err
	}
	for _, step := range steps {
		switch step.change.Kind {
		case deltaKindDelete:
			// A delete must not traverse a symlinked parent out of the repo
			// either. A missing parent makes the delete a no-op.
			if err := checkExistingParentInsideRoot(root, step.abs); err != nil {
				return fmt.Errorf("git delta: delete %s: %w", step.change.Path, err)
			}
			if err := os.Remove(step.abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("git delta: delete %s: %w", step.change.Path, err)
			}
		case deltaKindAdd, deltaKindModify:
			if err := ensureParentInsideRoot(root, step.abs); err != nil {
				return fmt.Errorf("git delta: write %s: %w", step.change.Path, err)
			}
			if err := atomicWriteFile(step.abs, step.change.Content, step.change.Mode.Perm()); err != nil {
				return fmt.Errorf("git delta: write %s: %w", step.change.Path, err)
			}
			// atomicWriteFile preserves an existing file's mode by design.
			// Settlement is the opposite case: the manifest is the authority,
			// so a mode-only change must land.
			if err := os.Chmod(step.abs, step.change.Mode.Perm()); err != nil {
				return fmt.Errorf("git delta: chmod %s: %w", step.change.Path, err)
			}
		}
	}
	return nil
}

// ensureParentInsideRoot refuses to create or write through a symlinked parent
// that resolves outside the repository root, then creates the parents. The check
// runs before MkdirAll (so a symlinked ancestor cannot be followed to create
// directories outside) and again after it (so a concurrent swap cannot slip
// through).
func ensureParentInsideRoot(root, abs string) error {
	dir := filepath.Dir(abs)
	probe := dir
	for {
		if _, err := os.Lstat(probe); err == nil {
			break
		}
		next := filepath.Dir(probe)
		if next == probe {
			break
		}
		probe = next
	}
	if err := checkResolvedInsideRoot(root, probe); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return checkResolvedInsideRoot(root, dir)
}

// checkExistingParentInsideRoot guards a delete: removing a symlinked parent's
// child would otherwise delete outside the repository. A parent that does not
// exist makes the delete a no-op, which is safe.
func checkExistingParentInsideRoot(root, abs string) error {
	parent := filepath.Dir(abs)
	if _, err := os.Lstat(parent); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return checkResolvedInsideRoot(root, parent)
}

// checkResolvedInsideRoot compares a path's resolved form with the resolved
// repository root.
func checkResolvedInsideRoot(root, p string) error {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve repo root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return err
	}
	if !withinRoot(resolved, resolvedRoot) {
		return fmt.Errorf("path resolves outside the repository root (%s)", resolvedRoot)
	}
	return nil
}

// hostChangedSinceBasis lists the paths that changed on the host after the
// dispatch basis was captured. The basis state carries the host's dirty tree at
// dispatch, so a pre-existing modification that the proposal also touches is
// not a collision — the proposal built on that exact content.
func (p *retainedProposal) hostChangedSinceBasis(ctx context.Context) ([]string, error) {
	if p.basisState.Backend == "" {
		return nil, fmt.Errorf("no recorded host basis; use discard")
	}
	if p.mgr == nil || p.parentID == "" {
		return nil, fmt.Errorf("the host root scope is no longer available")
	}
	parent, ok := p.mgr.Get(p.parentID)
	if !ok {
		return nil, fmt.Errorf("the host root scope is no longer available")
	}
	sb := parent.Sandbox()
	if sb == nil {
		return nil, fmt.Errorf("the host root has no workspace sandbox")
	}
	_, files, err := sb.Diff(ctx, p.basisState, 0)
	if err != nil {
		return nil, err
	}
	return normalizeHostDiffPaths(sb.Backend(), files)
}

// normalizeHostDiffPaths maps a host sandbox's diff output onto repo-relative
// slash paths for the collision guard. The git backend already reports
// workdir-relative paths; a snapshot-style backend reports absolute in-sandbox
// paths, which would compare unequal to every manifest path and silently empty
// the overlap set. Refuse anything but git rather than fail open.
func normalizeHostDiffPaths(backend string, paths []string) ([]string, error) {
	if backend != "git" {
		return nil, fmt.Errorf("host root backend is %q; basis-relative collision checks are only defined for the git backend", backend)
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, path.Clean(filepath.ToSlash(p)))
	}
	return out, nil
}

// hostChangedPaths lists the paths the host working tree has changed relative to
// its HEAD, including untracked files. It is a diagnostic/test helper, not
// apply's guard: apply compares against the dispatch basis (hostChangedSinceBasis),
// because pre-existing dirty paths are part of that basis. This helper's
// --untracked-files=all view is therefore broader than the guard's view.
//
// A repository git cannot read is an error rather than an empty list, so a
// caller cannot mistake an unreadable repository for "nothing changed".
func hostChangedPaths(ctx context.Context, repoPath string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath,
		"status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git status: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	fields := strings.Split(string(out), "\x00")
	var paths []string
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if f == "" {
			continue
		}
		if len(f) < 4 || f[2] != ' ' {
			return nil, fmt.Errorf("unexpected git status entry %q", f)
		}
		paths = append(paths, path.Clean(filepath.ToSlash(f[3:])))
		// --no-renames should make this unreachable, but a rename record
		// carries a second path and silently ignoring it would under-report
		// the host's changes.
		if f[0] == 'R' || f[0] == 'C' || f[1] == 'R' || f[1] == 'C' {
			if i+1 < len(fields) && fields[i+1] != "" {
				i++
				paths = append(paths, path.Clean(filepath.ToSlash(fields[i])))
			}
		}
	}
	return paths, nil
}

// overlappingPaths returns the manifest paths that the host has also changed,
// sorted. Comparison is on the cleaned slash path, the same normalization both
// producers emit.
func overlappingPaths(deltaPaths, hostPaths []string) []string {
	set := make(map[string]bool, len(hostPaths))
	for _, p := range hostPaths {
		set[pathKey(path.Clean(filepath.ToSlash(p)))] = true
	}
	var overlap []string
	for _, p := range deltaPaths {
		if set[pathKey(path.Clean(p))] {
			overlap = append(overlap, p)
		}
	}
	sort.Strings(overlap)
	return overlap
}

// writeProposalMarker records a settlement transition inside the artifact
// directory. The applying marker makes a crash mid-settlement detectable on
// rehydration; the settled marker makes a crash between the host write and the
// directory removal harmless. Both are fsynced: a marker that only lives in the
// page cache cannot serve that purpose.
func writeProposalMarker(dir, name string, payload map[string]any) error {
	if dir == "" {
		return errors.New("no artifact directory")
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(dir, name)
	if err := atomicWriteFile(p, data, 0o600); err != nil {
		return err
	}
	return syncFile(p)
}

// applyGitDeltaWorkspace applies a manifest to a sandbox workspace instead of
// the host tree. Whole-path post-images only; the workspace's ResolvePath
// enforces the container root, and no host filesystem primitive is used.
func applyGitDeltaWorkspace(ctx context.Context, ws Workspace, delta *GitDelta) error {
	if err := validateDelta(delta); err != nil {
		return err
	}
	for _, change := range delta.Changes {
		rel, err := safeDeltaPath(change.Path)
		if err != nil {
			return err
		}
		abs, err := ws.ResolvePath(rel)
		if err != nil {
			return fmt.Errorf("resolve %s: %w", change.Path, err)
		}
		switch change.Kind {
		case deltaKindDelete:
			if err := ws.Remove(ctx, abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("delete %s: %w", change.Path, err)
			}
		case deltaKindAdd, deltaKindModify:
			parent := path.Dir(abs)
			// Never chmod an existing parent: sandboxWorkspace.MkdirAll
			// unconditionally applies its mode, while the host os.MkdirAll
			// leaves existing directories alone. Only create when missing.
			if _, statErr := ws.Stat(ctx, parent); statErr != nil {
				if !errors.Is(statErr, fs.ErrNotExist) {
					return fmt.Errorf("stat parent for %s: %w", change.Path, statErr)
				}
				if err := ws.MkdirAll(ctx, parent, 0o755); err != nil {
					return fmt.Errorf("create parent for %s: %w", change.Path, err)
				}
			}
			if err := ws.WriteFile(ctx, abs, change.Content, change.Mode.Perm()); err != nil {
				return fmt.Errorf("write %s: %w", change.Path, err)
			}
		default:
			return fmt.Errorf("unknown change kind %q", change.Kind)
		}
	}
	return nil
}

// requireSessionUnmoved enforces select's precondition for a session-target
// proposal: the session container must still digest to the basis captured at
// dispatch.
func (p *retainedProposal) requireSessionUnmoved(ctx context.Context) error {
	if p.session == nil {
		return fmt.Errorf("proposal %s: no live session container to settle into", p.id)
	}
	if !p.session.Releasable() {
		return fmt.Errorf("proposal %s: session backend cannot release a captured state", p.id)
	}
	state, err := p.session.Capture(ctx)
	if err != nil {
		return fmt.Errorf("proposal %s: select cannot capture the session state: %w", p.id, err)
	}
	digest, digErr := state.Digest()
	releaseErr := p.session.ReleaseCapture(context.WithoutCancel(ctx), state)
	if digErr != nil {
		return fmt.Errorf("proposal %s: select cannot digest the session state: %w", p.id, digErr)
	}
	if releaseErr != nil {
		return fmt.Errorf("proposal %s: select cannot release the session capture: %w", p.id, releaseErr)
	}
	if digest != p.basisDigest {
		return fmt.Errorf("proposal %s: the session container moved since the dispatch; select applies only onto the dispatch basis — use apply, or discard", p.id)
	}
	return nil
}

// sessionChangedSinceBasis lists the session-container paths changed after the
// dispatch basis, normalized to workspace-relative slash paths.
func (p *retainedProposal) sessionChangedSinceBasis(ctx context.Context) ([]string, error) {
	if p.session == nil {
		return nil, fmt.Errorf("proposal %s: no live session container to settle into", p.id)
	}
	_, files, err := p.session.sb.Diff(ctx, p.basisState, 0)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, path.Clean(filepath.ToSlash(p.session.rootRel(f))))
	}
	return out, nil
}

// settleGitDeltaProposal settles a container proposal. The container is already
// gone (destroyed at seal); the manifest is the proposal. select and apply write
// the host tree for a host-target proposal, or the session container for a
// session-target one; release exports the manifest and discard drops it.
func (r *ProposalRegistry) settleGitDeltaProposal(ctx context.Context, p *retainedProposal, action string) (string, error) {
	verb, ok := settlementVerb(action)
	if !ok {
		return "", fmt.Errorf("proposal: unknown action %q (select, apply, release, discard)", action)
	}
	target := p.target
	if target == "" {
		target = "host"
	}
	sessionTarget := target == "session"
	if sessionTarget && p.session == nil {
		return "", fmt.Errorf("proposal %s: session target has no live session container", p.id)
	}
	env := proposalEnvelope{
		Status:     "settled",
		ProposalID: p.id,
		Action:     action,
		Files:      p.delta.Paths(),
		World:      string(subagent.WorldContainer),
		Target:     target,
	}
	if !sessionTarget {
		// The proposal directory must be invisible to git before select
		// captures the host state; a new session may not have run the
		// seal-time exclusion. A session target never captures host git.
		if err := ensureProposalPathsExcluded(ctx, p.repoPath); err != nil {
			return "", fmt.Errorf("proposal %s: exclude proposal path: %w", p.id, err)
		}
	}
	if err := writeProposalMarker(p.artifactDir, proposalApplying, map[string]any{
		"proposal_id":     p.id,
		"action":          action,
		"manifest_digest": p.deltaDigest,
	}); err != nil {
		return "", fmt.Errorf("proposal %s: cannot record settlement start: %w", p.id, err)
	}
	applied := false
	defer func() {
		if !applied {
			// The action failed before changing anything durable enough to
			// consume the proposal; leave it retryable and drop the start
			// marker so rehydration does not quarantine it.
			_ = os.Remove(filepath.Join(p.artifactDir, proposalApplying))
		}
	}()

	switch verb {
	case goatherd.SettleSelect:
		var err error
		if sessionTarget {
			err = p.requireSessionUnmoved(ctx)
		} else {
			err = p.requireHostUnmoved(ctx)
		}
		if err != nil {
			return "", err
		}
		if sessionTarget {
			err = applyGitDeltaWorkspace(ctx, p.session.Workspace(), p.delta)
		} else {
			err = applyGitDelta(ctx, p.repoPath, p.delta)
		}
		if err != nil {
			return "", fmt.Errorf("proposal %s: select: %w", p.id, err)
		}
	case goatherd.SettleApply:
		// Changes *since the dispatch basis*, not everything dirty: the parent
		// is allowed to arrive at settlement already dirty (the basis captured
		// that state), and only work done after the dispatch can collide.
		var changed []string
		var err error
		if sessionTarget {
			changed, err = p.sessionChangedSinceBasis(ctx)
		} else {
			changed, err = p.hostChangedSinceBasis(ctx)
		}
		if err != nil {
			return "", fmt.Errorf("proposal %s: apply: cannot determine the parent's changes since the dispatch, refusing to write: %w", p.id, err)
		}
		if overlap := overlappingPaths(p.delta.Paths(), changed); len(overlap) > 0 {
			return "", fmt.Errorf("proposal %s: the parent changed paths this proposal also changes (%s); apply is path-disjoint only — resolve the overlap, then apply or discard",
				p.id, strings.Join(overlap, ", "))
		}
		if sessionTarget {
			err = applyGitDeltaWorkspace(ctx, p.session.Workspace(), p.delta)
		} else {
			err = applyGitDelta(ctx, p.repoPath, p.delta)
		}
		if err != nil {
			return "", fmt.Errorf("proposal %s: apply: %w", p.id, err)
		}
	case goatherd.SettleRelease:
		retained, err := exportGitDelta(ctx, p)
		if err != nil {
			return "", fmt.Errorf("proposal %s: release could not preserve the manifest: %w", p.id, err)
		}
		env.Retained = retained
	case goatherd.SettleDiscard:
		// Nothing to write; the manifest is simply consumed.
	}
	applied = true

	// The settlement must be legible in the causal record even after the
	// artifact directory is gone. Append to the host root scope: the dispatch
	// scope is already discarded, and this is where a worktree settlement
	// records its sealed output.
	if p.mgr != nil && p.parentID != "" {
		if scope, ok := p.mgr.Get(p.parentID); ok {
			if _, ferr := scope.AppendFact(ctx, SchemaProposalSettled, "proposal", map[string]any{
				"proposal_id":     p.id,
				"action":          action,
				"world":           string(subagent.WorldContainer),
				"target":          target,
				"manifest_digest": p.deltaDigest,
				"files":           p.delta.Paths(),
			}); ferr != nil {
				env.Notes = appendSettleNote(env.Notes, "trace: settle fact: "+ferr.Error())
			}
		} else {
			env.Notes = appendSettleNote(env.Notes, "trace: settle fact skipped: host root scope is gone")
		}
	}

	if err := writeProposalMarker(p.artifactDir, proposalSettled, map[string]any{
		"proposal_id":     p.id,
		"action":          action,
		"manifest_digest": p.deltaDigest,
	}); err != nil {
		env.Notes = appendSettleNote(env.Notes, "settlement marker: "+err.Error())
	}

	// Consume-once: the proposal and its artifact directory go away together.
	// A removal failure does not undo a write that already landed; it is a
	// caveat on a settled proposal, not a reason to report failure.
	if p.artifactDir != "" {
		if err := os.RemoveAll(p.artifactDir); err != nil {
			env.Notes = appendSettleNote(env.Notes, "proposal directory removal failed: "+err.Error())
		}
	}
	r.remove(p.id)
	env.Outcome = gitDeltaSettleOutcome(verb, env).String()
	return marshalProposalEnvelope(env), nil
}

// requireHostUnmoved enforces select's precondition: the host's captured
// workspace state must still digest equal to the state captured at dispatch.
// A moved host fails closed rather than fast-forwarding over work it never saw.
func (p *retainedProposal) requireHostUnmoved(ctx context.Context) error {
	if p.mgr == nil || p.parentID == "" {
		return fmt.Errorf("proposal %s: select needs the host basis captured at dispatch", p.id)
	}
	if p.basisDigest == "" {
		return fmt.Errorf("proposal %s: select has no recorded host basis; use apply or discard", p.id)
	}
	parent, ok := p.mgr.Get(p.parentID)
	if !ok {
		return fmt.Errorf("proposal %s: the host root scope is no longer available; use discard", p.id)
	}
	state, err := parent.CaptureWorkspace(ctx)
	if err != nil {
		return fmt.Errorf("proposal %s: select cannot capture the host state: %w", p.id, err)
	}
	digest, err := state.Digest()
	if err != nil {
		return fmt.Errorf("proposal %s: select cannot digest the host state: %w", p.id, err)
	}
	if digest != p.basisDigest {
		return fmt.Errorf("proposal %s: the host tree moved since the dispatch; select applies only onto the dispatch basis — use apply, or discard", p.id)
	}
	return nil
}

// gitDeltaSettleOutcome renders the typed Outcome for a container settlement.
func gitDeltaSettleOutcome(verb goatherd.SettlementAction, env proposalEnvelope) Outcome {
	oc := Outcome{Scope: strings.Join(env.Files, ", "), Record: "proposal " + env.ProposalID, Notes: env.Notes}
	where := "the host tree"
	if env.Target == "session" {
		where = "the session container"
	}
	switch verb {
	case goatherd.SettleSelect:
		oc.Work = "container delta landed on " + where
	case goatherd.SettleApply:
		oc.Work = "container delta applied path-disjoint onto " + where
	case goatherd.SettleRelease:
		oc.Work = "container delta exported; the container world was destroyed at seal"
		oc.Record = "retained " + env.Retained
	case goatherd.SettleDiscard:
		oc.Work = "container delta discarded"
		oc.Record = ""
	}
	return oc
}

// appendSettleNote joins settlement caveats without dropping an earlier one.
func appendSettleNote(existing, note string) string {
	switch {
	case existing == "":
		return note
	case note == "":
		return existing
	default:
		return existing + "; " + note
	}
}
