package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Overwrite stash: the recovery half of the explicit-replace rule (yaah-5hkb).
//
// A guard that refuses a blind overwrite is only useful if the deliberate
// overwrite is recoverable: the caller passes overwrite: true, the previous
// bytes are the thing most likely to be missed a turn later, and a stash means
// a mistaken replace is one copy away instead of a rewrite from memory. The
// stash is deliberately not version control (the file may never have been
// committed) and not a backup feature: it is the last-overwritten copy, bounded,
// inside the workspace that owns the file.
//
// It lives under .scratch/ — the workspace's documented disposable area — rather
// than under the yaah home, because every file operation a tool performs has to
// route through the Workspace interface (internal/tools/enforcement_test.go
// enforces that): a stash is a write like any other, and it belongs to the same
// confined world as the file it protects. For a sub-agent's isolated dispatch
// world that also means the stash is discarded with the work it held, which is
// the right lifetime.

const (
	// overwriteStashRelDir is the stash location, relative to the workspace
	// root. `.scratch/` is the documented disposable area, and it is ignored by
	// git in this repository, so a stash never shows up as untracked work.
	overwriteStashRelDir = ".scratch/overwritten"

	// overwriteStashKeep bounds the stash. A runaway loop that overwrites the
	// same file every turn must not fill the disk; 100 covers any realistic
	// "undo that" window and stays small enough to list.
	overwriteStashKeep = 100
)

// stashOverwrite keeps the bytes an overwrite is about to destroy and returns the
// path they landed at.
//
// The name carries the wall clock first (so a directory listing sorts
// chronologically), a short digest of the original path (so two same-named files
// from different directories are distinguishable without storing the path
// itself), and the base name (so the entry is recognizable to a human).
//
// An error aborts the overwrite: a caller that asked to replace a file is better
// served by a refusal it can retry than by an unrecoverable replace.
func stashOverwrite(ctx context.Context, ws Workspace, originalPath string, data []byte) (string, error) {
	dir, err := ws.ResolveWritePath(overwriteStashRelDir)
	if err != nil {
		return "", fmt.Errorf("stash previous content: resolve %s: %w", overwriteStashRelDir, err)
	}
	if err := ws.MkdirAll(ctx, dir, 0o700); err != nil {
		return "", fmt.Errorf("stash previous content: create %s: %w", dir, err)
	}
	name := fmt.Sprintf("%s-%s-%s",
		time.Now().UTC().Format("20060102T150405.000000000"),
		pathFingerprint(originalPath),
		filepathBase(originalPath))
	path := ws.Join(dir, name)
	if err := ws.WriteFile(ctx, path, data, 0o600); err != nil {
		return "", fmt.Errorf("stash previous content: write %s: %w", path, err)
	}
	pruneOverwriteStash(ctx, ws, dir, overwriteStashKeep)
	return path, nil
}

// pruneOverwriteStash keeps the newest keep entries and removes the rest. It is
// best effort: a failed prune must not fail the write the caller already asked
// for, so the error is dropped — a stash directory that cannot be pruned is not
// a reason to lose an edit.
func pruneOverwriteStash(ctx context.Context, ws Workspace, dir string, keep int) {
	entries, err := ws.ReadDir(ctx, dir)
	if err != nil || len(entries) <= keep {
		return
	}
	type entry struct {
		name string
		mod  time.Time
	}
	all := make([]entry, 0, len(entries))
	for _, e := range entries {
		info, infoErr := e.Info()
		if infoErr != nil {
			continue
		}
		all = append(all, entry{name: e.Name(), mod: info.ModTime()})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mod.After(all[j].mod) })
	for i := keep; i < len(all); i++ {
		_ = ws.Remove(ctx, ws.Join(dir, all[i].name))
	}
}

// filepathBase returns the last element of a path in either syntax. Tool paths
// are already resolved for a workspace that may be POSIX inside a Windows host,
// so filepath.Base would be wrong for a sandbox path on Windows.
func filepathBase(path string) string {
	trimmed := strings.TrimRight(path, `/\`)
	if i := strings.LastIndexAny(trimmed, `/\`); i >= 0 {
		return trimmed[i+1:]
	}
	return trimmed
}

// pathFingerprint is the short hex prefix of a path's digest: it makes two
// same-named targets distinguishable in the stash without storing the path.
func pathFingerprint(path string) string {
	sum := sha256.Sum256([]byte(filepathToSlash(path)))
	return hex.EncodeToString(sum[:])[:8]
}

// filepathToSlash normalizes either separator so a path fingerprints the same on
// every platform and inside a sandbox.
func filepathToSlash(path string) string {
	return strings.ReplaceAll(path, `\`, "/")
}

// fileContentDigest is the full sha256 hex digest of data, the form the write
// guard and the receipts use.
func fileContentDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// digestMatches reports whether the caller's if_digest identifies digest.
//
// Accepted forms are the ones a caller can actually copy out of this tool's
// output: the receipt's `sha256:<12 hex>` prefix, a bare hex digest, or any hex
// prefix of at least 12 characters (the length the receipts print). Shorter
// prefixes are rejected rather than guessed at — 8 hex characters collide often
// enough that accepting them would turn a precondition into a coin flip.
func digestMatches(given, digest string) bool {
	want := strings.ToLower(strings.TrimSpace(given))
	want = strings.TrimPrefix(want, "sha256:")
	const minPrefix = 12
	if len(want) < minPrefix || len(want) > len(digest) {
		return false
	}
	return strings.HasPrefix(digest, want)
}

// lineCount counts the lines in data the way an editor would report them: the
// number of separators plus a final partial line, and zero for empty content.
func lineCount(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := 1
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	if data[len(data)-1] == '\n' {
		n--
	}
	return n
}
