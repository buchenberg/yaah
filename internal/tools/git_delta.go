package tools

import (
	"context"
	"encoding/base64"
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
	"strconv"
	"strings"
	"unicode/utf8"

	goatherd "github.com/buchenberg/goatherd-runtime"
	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
	"github.com/buchenberg/yaah/internal/agent/subagent"
)

// git_delta.go implements the cross-backend settlement artifact for a
// container proposal (docs/shepherd-parity.md §Container settlement: GitDelta,
// decision D11). The parent world is a git sandbox and the dispatch world is a
// container, so there is no shared state language to compare or merge. The
// artifact is instead a change manifest of whole-path post-images produced by
// git plumbing inside the dispatch container.
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

// GitDelta is a container proposal's settlement artifact: the in-container
// baseline commit it was computed against, and one whole-path post-image per
// changed path.
type GitDelta struct {
	// BasisHead is the in-container baseline commit. The bootstrap creates a
	// synthetic commit for a dirty host tree, so this is the exact basis the
	// body started from.
	BasisHead string        `json:"basis_head"`
	Changes   []DeltaChange `json:"changes"`
}

// DeltaChange is one changed path in a GitDelta. Content is the post-image and
// is empty for a delete.
type DeltaChange struct {
	// Path is slash-separated and repo-relative.
	Path string `json:"path"`
	// Kind is "add", "modify", or "delete".
	Kind string `json:"kind"`
	// Mode is the post-image mode (the pre-image mode for a delete; git only
	// tracks regular-file modes 0644 and 0755).
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

// rawDiffEntry is one NUL-delimited record of
// `git diff-tree -r --raw -z --no-commit-id --no-renames`.
type rawDiffEntry struct {
	OldMode string
	NewMode string
	OldSHA  string
	NewSHA  string
	Status  string
	Path    string
}

// parseGitRawDiff parses git's raw diff output. The format is
// ":<oldmode> <newmode> <oldsha> <newsha> <status>\0<path>\0"; with -z paths are
// NUL-terminated and never quoted, so a path containing a newline round-trips.
//
// Rename and copy records are rejected rather than guessed at: the producer
// passes --no-renames, and a surprising record means the assumption that makes
// the manifest exact no longer holds.
func parseGitRawDiff(raw string) ([]rawDiffEntry, error) {
	fields := strings.Split(raw, "\x00")
	var out []rawDiffEntry
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if f == "" {
			continue // the trailing terminator (or an empty record)
		}
		if !strings.HasPrefix(f, ":") {
			return nil, fmt.Errorf("git delta: malformed raw diff record %q (want a ':' header)", f)
		}
		parts := strings.Fields(f[1:])
		if len(parts) != 5 {
			return nil, fmt.Errorf("git delta: malformed raw diff header %q (want 5 fields)", f)
		}
		e := rawDiffEntry{
			OldMode: parts[0],
			NewMode: parts[1],
			OldSHA:  parts[2],
			NewSHA:  parts[3],
			Status:  parts[4],
		}
		if i+1 >= len(fields) {
			return nil, fmt.Errorf("git delta: raw diff header %q has no path", f)
		}
		e.Path = fields[i+1]
		i++
		if e.Path == "" {
			return nil, fmt.Errorf("git delta: raw diff header %q has an empty path", f)
		}
		if strings.HasPrefix(e.Status, "R") || strings.HasPrefix(e.Status, "C") {
			return nil, fmt.Errorf("git delta: rename/copy record for %q is not supported (the producer must pass --no-renames)", e.Path)
		}
		out = append(out, e)
	}
	return out, nil
}

// gitChangeKind maps git's status letter to a manifest kind. Anything else — a
// typechange, an unmerged entry, a malformed status — is refused rather than
// mapped onto the wrong post-image.
func gitChangeKind(status string) (string, error) {
	if status == "" {
		return "", errors.New("git delta: empty status in raw diff")
	}
	switch status[0] {
	case 'A':
		return deltaKindAdd, nil
	case 'M':
		return deltaKindModify, nil
	case 'D':
		return deltaKindDelete, nil
	case 'T':
		return "", fmt.Errorf("git delta: typechange record %q is not representable as a whole-path file post-image", status)
	default:
		return "", fmt.Errorf("git delta: unsupported diff status %q", status)
	}
}

// parseGitMode converts git's octal mode to an fs.FileMode. Only regular files
// are supported: a symlink (120000) or gitlink (160000) cannot be written by
// the manifest without changing the host's meaning.
func parseGitMode(mode string) (fs.FileMode, error) {
	switch mode {
	case "100644", "100755":
		u, err := strconv.ParseUint(mode, 8, 32)
		if err != nil {
			return 0, fmt.Errorf("git delta: invalid mode %q: %w", mode, err)
		}
		return fs.FileMode(u & 0o777), nil
	case "000000":
		return 0, nil
	default:
		return 0, fmt.Errorf("git delta: unsupported mode %q (only regular files 100644/100755 are settled)", mode)
	}
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
			if _, err := parseGitMode(fmt.Sprintf("100%03o", c.Mode.Perm())); err != nil {
				return fmt.Errorf("git delta: %s: mode %04o is not a regular-file mode", c.Path, c.Mode.Perm())
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

// containerGitHead reads the in-container HEAD. Captured before the body, it is
// the basis the manifest is computed against: the bootstrap commits a synthetic
// baseline for a dirty host tree, so HEAD is exact either way.
func containerGitHead(ctx context.Context, sb goatherdsandbox.Sandbox, root string) (string, error) {
	out, err := runSandboxGit(ctx, sb, root, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(out)
	if head == "" {
		return "", errors.New("git delta: git rev-parse HEAD returned nothing")
	}
	return head, nil
}

// runSandboxGit runs one git command in the sandbox and maps a non-zero exit to
// an error that names the command and its stderr.
func runSandboxGit(ctx context.Context, sb goatherdsandbox.Sandbox, root string, args ...string) (string, error) {
	res, err := sb.Exec(ctx, goatherdsandbox.ExecRequest{Command: "git", Args: args, Cwd: root})
	if err != nil {
		return "", fmt.Errorf("git delta: git %s: %w", strings.Join(args, " "), err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("git delta: git %s: exit %d: %s", strings.Join(args, " "), res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return res.Stdout, nil
}

// captureGitDelta produces the manifest for a finished body. It stages the whole
// tree, writes the tree object, and diffs raw against the basis. Post-image
// blobs are read through base64 so binary content survives the exec transport.
//
// The returned review is git's own unified diff (capped, display-only). It is
// not the settlement artifact; it exists so `show` stays useful after the
// container is gone.
func captureGitDelta(ctx context.Context, sb goatherdsandbox.Sandbox, root, base string) (*GitDelta, string, error) {
	if strings.TrimSpace(base) == "" {
		return nil, "", errors.New("git delta: no in-container baseline HEAD to diff against")
	}
	if _, err := runSandboxGit(ctx, sb, root, "add", "-A"); err != nil {
		return nil, "", fmt.Errorf("git delta: stage working tree: %w", err)
	}
	treeOut, err := runSandboxGit(ctx, sb, root, "write-tree")
	if err != nil {
		return nil, "", fmt.Errorf("git delta: write tree: %w", err)
	}
	tree := strings.TrimSpace(treeOut)
	if tree == "" {
		return nil, "", errors.New("git delta: git write-tree returned nothing")
	}
	raw, err := runSandboxGit(ctx, sb, root,
		"diff-tree", "-r", "--raw", "-z", "--no-commit-id", "--no-renames", base, tree)
	if err != nil {
		return nil, "", fmt.Errorf("git delta: diff tree: %w", err)
	}
	entries, err := parseGitRawDiff(raw)
	if err != nil {
		return nil, "", err
	}
	if len(entries) > gitDeltaMaxFiles {
		return nil, "", fmt.Errorf("git delta: %d changed files exceeds the %d-file cap", len(entries), gitDeltaMaxFiles)
	}

	delta := &GitDelta{BasisHead: base, Changes: make([]DeltaChange, 0, len(entries))}
	total := 0
	for _, e := range entries {
		change, err := deltaChangeFromRaw(ctx, sb, root, e, gitDeltaMaxContentBytes-total)
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

	// Display-only review. A failure here must not fail the seal: the manifest
	// is complete and exact, and the fallback renderer can still show it.
	review := ""
	if len(delta.Changes) > 0 {
		if out, rerr := runSandboxGit(ctx, sb, root,
			"diff", "--cached", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", base); rerr == nil {
			review = clipDeltaReview(out)
		}
	}
	return delta, review, nil
}

// deltaChangeFromRaw turns one raw record into a manifest change. Deletes carry
// the pre-image mode and no content; adds and modifies read the post-image blob
// after checking its size against the remaining manifest budget, so a huge blob
// is refused before it is transferred.
func deltaChangeFromRaw(ctx context.Context, sb goatherdsandbox.Sandbox, root string, e rawDiffEntry, remaining int) (DeltaChange, error) {
	kind, err := gitChangeKind(e.Status)
	if err != nil {
		return DeltaChange{}, err
	}
	p, err := safeDeltaPath(e.Path)
	if err != nil {
		return DeltaChange{}, err
	}
	switch kind {
	case deltaKindDelete:
		mode, merr := parseGitMode(e.OldMode)
		if merr != nil {
			return DeltaChange{}, fmt.Errorf("git delta: %s: %w", p, merr)
		}
		return DeltaChange{Path: p, Kind: kind, Mode: mode}, nil
	default:
		mode, merr := parseGitMode(e.NewMode)
		if merr != nil {
			return DeltaChange{}, fmt.Errorf("git delta: %s: %w", p, merr)
		}
		size, serr := sandboxBlobSize(ctx, sb, root, e.NewSHA)
		if serr != nil {
			return DeltaChange{}, fmt.Errorf("git delta: %s: %w", p, serr)
		}
		if size > int64(remaining) {
			return DeltaChange{}, fmt.Errorf("git delta: post-image content exceeds the %d-byte cap; refusing to truncate", gitDeltaMaxContentBytes)
		}
		data, berr := readSandboxBlob(ctx, sb, root, e.NewSHA)
		if berr != nil {
			return DeltaChange{}, fmt.Errorf("git delta: %s: %w", p, berr)
		}
		if int64(len(data)) != size {
			return DeltaChange{}, fmt.Errorf("git delta: %s: blob read %d bytes, expected %d", p, len(data), size)
		}
		return DeltaChange{Path: p, Kind: kind, Mode: mode, Content: data}, nil
	}
}

// sandboxBlobSize reports a blob's size without transferring it.
func sandboxBlobSize(ctx context.Context, sb goatherdsandbox.Sandbox, root, sha string) (int64, error) {
	out, err := runSandboxGit(ctx, sb, root, "cat-file", "-s", sha)
	if err != nil {
		return 0, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil || size < 0 {
		return 0, fmt.Errorf("git cat-file -s %s: unexpected size %q", sha, strings.TrimSpace(out))
	}
	return size, nil
}

// readSandboxBlob reads a git blob as base64 inside the shell and decodes it in
// Go. Raw binary on stdout can be mangled by an exec transport; base64 makes the
// bytes explicit.
// readSandboxBlob reads one blob as base64 over the sandbox transport.
//
// The producer currently costs two execs per changed file (a size probe, then
// this read). A `git cat-file --batch` stream would return size and content in
// one round trip; it is a worthwhile optimisation if proposals routinely carry
// many small files, but not a correctness issue (the caps bound the cost).
func readSandboxBlob(ctx context.Context, sb goatherdsandbox.Sandbox, root, sha string) ([]byte, error) {
	if strings.TrimSpace(sha) == "" {
		return nil, errors.New("empty blob sha")
	}
	res, err := sb.Exec(ctx, goatherdsandbox.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", `git cat-file blob "$1" | base64`, "yaah", sha},
		Cwd:     root,
	})
	if err != nil {
		return nil, fmt.Errorf("git cat-file blob %s: %w", sha, err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("git cat-file blob %s: exit %d: %s", sha, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return decodeBase64Blob(res.Stdout)
}

// decodeBase64Blob removes the whitespace base64 tools insert and decodes the
// remainder.
func decodeBase64Blob(s string) ([]byte, error) {
	compact := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		}
		return r
	}, s)
	data, err := base64.StdEncoding.DecodeString(compact)
	if err != nil {
		return nil, fmt.Errorf("decode base64 blob: %w", err)
	}
	return data, nil
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
func persistGitDelta(ctx context.Context, repoPath, id string, delta *GitDelta, review string) (string, error) {
	if err := validateDelta(delta); err != nil {
		return "", err
	}
	if err := ensureProposalPathsExcluded(ctx, repoPath); err != nil {
		return "", fmt.Errorf("exclude proposal path from git: %w", err)
	}
	dir := proposalArtifactDir(repoPath, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create proposal dir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(delta, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal delta: %w", err)
	}
	deltaPath := filepath.Join(dir, "delta.json")
	if err := atomicWriteFile(deltaPath, data, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", deltaPath, err)
	}
	if review != "" {
		reviewPath := filepath.Join(dir, "review.diff")
		if err := atomicWriteFile(reviewPath, []byte(review), 0o600); err != nil {
			return "", fmt.Errorf("write %s: %w", reviewPath, err)
		}
	}
	// A crash after the seal must leave a readable manifest, so sync the
	// files and (best-effort) the directory. Directory fsync is not portable;
	// its failure cannot make the manifest unreadable, so it is not fatal.
	for _, p := range []string{deltaPath, filepath.Join(dir, "review.diff")} {
		if _, statErr := os.Stat(p); statErr != nil {
			continue
		}
		if err := syncFile(p); err != nil {
			return "", fmt.Errorf("sync %s: %w", p, err)
		}
	}
	_ = syncDir(dir)
	return dir, nil
}

// syncFile flushes one file's contents to stable storage.
func syncFile(p string) error {
	f, err := os.Open(p)
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
	for _, name := range []string{"delta.json", "review.diff"} {
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
// check. A repository git cannot read is an error: apply must fail closed rather
// than write over unknown changes.
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

// settleGitDeltaProposal settles a container proposal. The container is already
// gone (destroyed at seal); the manifest is the proposal. select and apply write
// the host tree, release exports the manifest, discard throws it away.
func (r *ProposalRegistry) settleGitDeltaProposal(ctx context.Context, p *retainedProposal, action string) (string, error) {
	verb, ok := settlementVerb(action)
	if !ok {
		return "", fmt.Errorf("proposal: unknown action %q (select, apply, release, discard)", action)
	}
	env := proposalEnvelope{
		Status:     "settled",
		ProposalID: p.id,
		Action:     action,
		Files:      p.delta.Paths(),
		World:      string(subagent.WorldContainer),
	}

	switch verb {
	case goatherd.SettleSelect:
		if err := p.requireHostUnmoved(ctx); err != nil {
			return "", err
		}
		if err := applyGitDelta(ctx, p.repoPath, p.delta); err != nil {
			return "", fmt.Errorf("proposal %s: select: %w", p.id, err)
		}
	case goatherd.SettleApply:
		// Changes *since the dispatch basis*, not everything dirty: the host
		// is allowed to arrive at settlement already dirty (the basis captured
		// that state), and only work done after the dispatch can collide.
		changed, err := p.hostChangedSinceBasis(ctx)
		if err != nil {
			return "", fmt.Errorf("proposal %s: apply: cannot determine the host's changes since the dispatch, refusing to write: %w", p.id, err)
		}
		if overlap := overlappingPaths(p.delta.Paths(), changed); len(overlap) > 0 {
			return "", fmt.Errorf("proposal %s: the host changed paths this proposal also changes (%s); apply is path-disjoint only — resolve the overlap, then apply or discard",
				p.id, strings.Join(overlap, ", "))
		}
		if err := applyGitDelta(ctx, p.repoPath, p.delta); err != nil {
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
	switch verb {
	case goatherd.SettleSelect:
		oc.Work = "container delta landed on the host tree"
	case goatherd.SettleApply:
		oc.Work = "container delta applied path-disjoint onto the host tree"
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
