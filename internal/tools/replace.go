package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"

	"github.com/buchenberg/yaah/internal/prompts"
)

// ReplaceTool performs regex find-and-replace across multiple files.
// It walks a directory tree, filters by include glob, and applies the
// replacement to every matching file.
type ReplaceTool struct {
	PV *PathValidator
	WS Workspace
}

// selfGuardsPath marks replace as taking an in-process write guard inside its
// own Execute (withFileWriteSet, fileguard.go), so the RecordingTool wrapper
// does not hold the same non-reentrant lock across it.
func (*ReplaceTool) selfGuardsPath() {}

var (
	_ PathValidatorSetter = (*ReplaceTool)(nil)
	_ WorkspaceSetter     = (*ReplaceTool)(nil)
)

func (t *ReplaceTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *ReplaceTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *ReplaceTool) Name() string { return "replace" }
func (t *ReplaceTool) Description() string {
	return prompts.ToolDescription("replace")
}

func (t *ReplaceTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "The regex pattern to search for"},
			"replacement": {"type": "string", "description": "The replacement text (supports $1, $2, etc. for capture groups)"},
			"path": {"type": "string", "description": "Directory to search in (default: current directory)"},
			"include": {"type": "string", "description": "File glob pattern to filter (e.g. \"*.go\", \"*.{ts,tsx}\")"},
			"dry_run": {"type": "boolean", "description": "If true, show what would change without writing files"}
		},
		"required": ["pattern", "replacement"]
	}`)
}

func (t *ReplaceTool) IsDangerous(argsJSON string) bool { return true }

func (t *ReplaceTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Pattern     string `json:"pattern"`
		Replacement string `json:"replacement"`
		Path        string `json:"path"`
		Include     string `json:"include"`
		DryRun      bool   `json:"dry_run"`
	}
	if err := StrictToolArgs("replace", t.Schema(), args, &params); err != nil {
		return "", err
	}
	if params.Pattern == "" {
		return "", fmt.Errorf("replace: pattern is required")
	}
	if params.Path == "" {
		params.Path = "."
	}
	ws := workspaceOf(t.WS, t.PV)
	resolved, err := ws.ResolveWritePath(params.Path)
	if err != nil {
		return "", err
	}
	params.Path = resolved

	re, err := regexp.Compile(params.Pattern)
	if err != nil {
		return "", fmt.Errorf("replace: invalid regex: %w", err)
	}

	var includeRe *regexp.Regexp
	if params.Include != "" {
		pat := globToRegex(params.Include)
		includeRe, err = regexp.Compile(pat)
		if err != nil {
			return "", fmt.Errorf("replace: invalid include pattern: %w", err)
		}
	}

	var report multiResult
	totalFiles := 0
	totalMatches := 0
	totalChanged := 0

	// Phase 1 walks the tree applying the per-file skip rules. A dry run is
	// read-only and completes here; a mutating run only collects candidate
	// paths and defers all reading and writing to phase 2, so every path this
	// call can write is covered by one set-lock acquisition.
	var candidates []string
	root := params.Path
	walkErr := walkWorkspace(ctx, ws, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && commonIgnoreDirs[d.Name()] {
				return filepath.SkipDir
			}
			if p != root && isNestedWorktreeRoot(ctx, ws, p) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if binaryExtensions[filepath.Ext(p)] {
			return nil
		}
		if includeRe != nil && !includeRe.MatchString(filepath.Base(p)) {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if params.DryRun {
			data, readErr := ws.ReadFile(ctx, p)
			if readErr != nil {
				report.failf("%s: read: %v", p, readErr)
				return nil
			}
			content := string(data)
			newContent := re.ReplaceAllString(content, params.Replacement)
			if newContent == content {
				return nil
			}
			matchCount := countMatches(re, content)
			report.donef("%s: %d match(es)", p, matchCount)
			totalFiles++
			totalMatches += matchCount
			return nil
		}

		candidates = append(candidates, p)
		return nil
	})

	if walkErr != nil && walkErr != filepath.SkipDir && ctx.Err() == nil {
		return "", fmt.Errorf("replace: %w", walkErr)
	}

	if ctx.Err() != nil {
		return "", fmt.Errorf("replace: %w", ctx.Err())
	}

	if params.DryRun {
		headline := fmt.Sprintf("DRY RUN — %d file(s) with %d match(es):", totalFiles, totalMatches)
		return report.Report(headline)
	}
	// Phase 2: one set acquisition over every candidate — the walk's
	// deterministic order feeds it, and the guard's sorted acquisition keeps
	// two overlapping replace calls from deadlocking — then each file's
	// read → substitute → verify → write runs under it. A dry run never gets
	// here; it wrote nothing in phase 1 and returned above.
	var staleErr *StaleWriteError
	withFileWriteSet(candidates, func() error {
		for _, p := range candidates {
			if ctx.Err() != nil {
				return nil
			}
			outcome, matchCount, receipt, ferr := replaceFileLocked(ctx, ws, p, re, params.Replacement)
			switch outcome {
			case replaceUnchanged:
				// The pattern did not change this file; stay silent, as the
				// walk always has.
			case replaceWritten:
				report.donef("%s: %d match(es), %s", p, matchCount, replaceReceiptTail(receipt))
				totalFiles++
				totalMatches += matchCount
				totalChanged++
			case replaceReadFailed:
				report.failf("%s: read: %v", p, ferr)
			case replaceWriteFailed:
				report.failf("%s: write: %v", p, ferr)
			case replaceStale:
				var stale *StaleWriteError
				if errors.As(ferr, &stale) {
					report.failf("%v", stale)
					if staleErr == nil {
						staleErr = stale
					}
				}
			}
		}
		return nil
	})
	if ctx.Err() != nil {
		return "", fmt.Errorf("replace: %w", ctx.Err())
	}

	headline := fmt.Sprintf("Replaced %d match(es) across %d file(s):", totalMatches, totalChanged)
	out, rerr := report.Report(headline)
	if staleErr != nil {
		// The report text already carries each refusal line; joining keeps
		// the typed error reachable through errors.As without rewriting the
		// report's shape.
		rerr = errors.Join(rerr, staleErr)
	}
	return out, rerr
}

func countMatches(re *regexp.Regexp, content string) int {
	return len(re.FindAllStringIndex(content, -1))
}

// replaceOutcome classifies one per-file result of a mutating replace run.
type replaceOutcome int

const (
	replaceUnchanged   replaceOutcome = iota // the pattern did not change the file
	replaceWritten                           // substituted and written
	replaceReadFailed                        // the read or the verify-read failed
	replaceWriteFailed                       // the write itself failed
	replaceStale                             // another writer changed the file mid-run
)

// replaceFileLocked applies the substitution to one file with the guard's
// compare-and-swap discipline: read the bytes and their digest, transform,
// re-read the digest, refuse with *StaleWriteError if an out-of-band writer
// changed the file in between, else write the new bytes with the file's
// existing permission bits (0o644 for a vanished path is unreachable — a
// missing file reports replaceReadFailed first). A written file also
// returns its receipt, built from the bytes this call already read and
// wrote — never from a third read of the file.
//
// It takes no lock of its own: the per-path lock is refcounted but not
// reentrant per goroutine, so this must only be called with the caller's
// withFileWriteSet already held over abs and its siblings — acquiring again
// here would deadlock the goroutine on itself. Calls to ws inside must not
// reach back into the guard (withFileWrite is fine only for paths outside
// the held set).
func replaceFileLocked(ctx context.Context, ws Workspace, abs string, re *regexp.Regexp, replacement string) (replaceOutcome, int, FileWriteReceipt, error) {
	before, cur, err := fileGuardSnapshot(ctx, ws, abs)
	if err != nil {
		return replaceReadFailed, 0, FileWriteReceipt{}, err
	}
	if !before.Exists {
		// The walk listed this file but it is gone by the read; surface it
		// the way a read failure always has.
		return replaceReadFailed, 0, FileWriteReceipt{}, fs.ErrNotExist
	}
	content := string(cur)
	newContent := re.ReplaceAllString(content, replacement)
	if newContent == content {
		return replaceUnchanged, 0, FileWriteReceipt{}, nil
	}
	matchCount := countMatches(re, content)

	// Compare-and-swap: the set lock only orders goroutines in this process,
	// so anything that raced past it — another process, an editor — is caught
	// here, before a byte is written.
	after, _, err := fileGuardSnapshot(ctx, ws, abs)
	if err != nil {
		return replaceReadFailed, matchCount, FileWriteReceipt{}, err
	}
	if after.Exists != before.Exists || after.Digest != before.Digest {
		return replaceStale, matchCount, FileWriteReceipt{}, &StaleWriteError{
			Path:     conflictWorkspaceRel(ws, abs),
			Expected: before.Digest,
			Found:    after.Digest,
		}
	}

	mode := before.Mode
	if mode == 0 {
		mode = 0o644
	}
	if err := ws.WriteFile(ctx, abs, []byte(newContent), mode); err != nil {
		return replaceWriteFailed, matchCount, FileWriteReceipt{}, err
	}
	// The receipt is built entirely from what this call already knows: the
	// bytes it read (verified by the compare-and-swap above) and the bytes
	// it just wrote, so the digest comes off the written content rather
	// than a third read of the file.
	sum := sha256.Sum256([]byte(newContent))
	return replaceWritten, matchCount, FileWriteReceipt{
		Path:        abs,
		BeforeBytes: len(cur),
		AfterBytes:  len(newContent),
		Digest:      hex.EncodeToString(sum[:]),
	}, nil
}

// replaceReceiptTail folds a written file's receipt into its per-file report
// line — byte delta plus a short digest — rather than adding a second line
// per file. The digest keeps the receipt's 12-hex prefix. A replace never
// creates files (a path missing at the read reports a failure before any
// write happens), so the created branch exists only to keep the shape
// honest.
func replaceReceiptTail(r FileWriteReceipt) string {
	digest := r.Digest
	if len(digest) > 12 {
		digest = digest[:12]
	}
	if r.Created || r.BeforeBytes < 0 {
		return fmt.Sprintf("created, %d bytes, sha256:%s", r.AfterBytes, digest)
	}
	return fmt.Sprintf("%d -> %d bytes, sha256:%s", r.BeforeBytes, r.AfterBytes, digest)
}
