package dispatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	goatherd "github.com/buchenberg/goatherd-runtime"
)

// proposal_merge.go implements the explicit worktree-only three-way apply
// strategy. It is never the default: the caller must ask for it, and a textual
// conflict leaves the parent and the proposal untouched. Container proposals do
// not have a merge primitive, so they keep whole-path path-disjoint apply.

const proposalMergeMaxBytes = 4 << 20

type proposalMergeWrite struct {
	path    string
	data    []byte
	del     bool
	mode    fs.FileMode
	modeSet bool
}

func (r *ProposalRegistry) settleWorktreeThreeWay(ctx context.Context, p *retainedProposal) (string, error) {
	if p.output == nil {
		return "", fmt.Errorf("proposal %s: three_way has no sealed worktree output", p.id)
	}
	if p.repoPath == "" {
		return "", fmt.Errorf("proposal %s: three_way has no repository path", p.id)
	}
	if state := p.output.State(); state != goatherd.OutputUnconsumed {
		return "", fmt.Errorf("proposal %s: three_way: sealed output is %s, not unconsumed", p.id, state)
	}
	changes, err := p.proposalChanges(ctx)
	if err != nil {
		return "", fmt.Errorf("proposal %s: three_way: %w", p.id, err)
	}
	drift, err := p.parentDrift(ctx)
	if err != nil {
		return "", fmt.Errorf("proposal %s: three_way: cannot inspect parent drift: %w", p.id, err)
	}
	driftSet := make(map[string]bool, len(drift))
	for _, path := range drift {
		driftSet[PathKey(normalizeProposalPath(path))] = true
	}
	// One EOL policy for the settle: core.autocrlf is read once here, and the
	// per-path .gitattributes probe only runs for a path whose bytes differ
	// while its folded content matches (PR #824 review, finding 3). The probe
	// is asked lazily from the comparison itself, so a byte-identical path
	// spawns nothing (round 3, finding 2).
	eol := newEOLNormalization(ctx, p.repoPath)

	var writes []proposalMergeWrite
	var conflicted []string
	var unbasisable []string
	for _, change := range changes {
		current, currentExists, err := p.parentContentFull(ctx, change.Path)
		if err != nil {
			return "", fmt.Errorf("proposal %s: three_way: read current %s: %w", p.id, change.Path, err)
		}
		proposed, proposedExists, err := p.proposedContentFull(ctx, change.Path)
		if err != nil {
			return "", fmt.Errorf("proposal %s: three_way: read proposed %s: %w", p.id, change.Path, err)
		}
		base, baseExists, err := p.baseContentFull(ctx, change.Path)
		if err != nil {
			// A base that cannot be read — a blob over the merge cap, a
			// non-git basis backend, a failing git plumbing call — leaves this
			// path unprovable, and none of the three strategies may run on a
			// guess. Aborting the whole settle strands every other path of the
			// proposal behind one unreadable blob; the no-drift fast path would
			// overwrite the parent whole-path with no basis to prove it
			// unmoved, which is the fail-open the content check exists to
			// prevent; and a three-way merge has no base input at all. So the
			// path is a conflict: it surfaces to the human, who can resolve or
			// discard, and the rest of the proposal is not hostage to it.
			conflicted = append(conflicted, change.Path)
			unbasisable = append(unbasisable, fmt.Sprintf("%s: %v", change.Path, err))
			continue
		}
		if !baseExists {
			base = nil
		}

		if !proposalPathDrifted(driftSet[PathKey(normalizeProposalPath(change.Path))], change.Path, eol, base, baseExists, current, currentExists) {
			if proposedExists {
				mode, modeSet := proposalModeFromString(change.Mode)
				writes = append(writes, proposalMergeWrite{
					path:    change.Path,
					data:    proposed,
					mode:    mode,
					modeSet: modeSet,
				})
			} else {
				writes = append(writes, proposalMergeWrite{path: change.Path, del: true})
			}
			continue
		}

		if !currentExists && !proposedExists {
			continue
		}
		if !currentExists || !proposedExists {
			conflicted = append(conflicted, change.Path)
			continue
		}
		if bytes.Equal(current, proposed) {
			continue
		}
		if len(base) > proposalMergeMaxBytes || len(current) > proposalMergeMaxBytes || len(proposed) > proposalMergeMaxBytes ||
			isBinaryContent(base) || isBinaryContent(current) || isBinaryContent(proposed) {
			conflicted = append(conflicted, change.Path)
			continue
		}
		merged, ok, err := gitMergeFile(ctx, base, current, proposed)
		if err != nil {
			return "", fmt.Errorf("proposal %s: three_way: merge %s: %w", p.id, change.Path, err)
		}
		if !ok {
			conflicted = append(conflicted, change.Path)
			continue
		}
		if !bytes.Equal(merged, current) {
			writes = append(writes, proposalMergeWrite{path: change.Path, data: merged})
		}
	}

	if len(conflicted) > 0 {
		message := "three-way apply found a textual conflict; resolve the path or use discard/release"
		if len(unbasisable) > 0 {
			message = fmt.Sprintf("%s; no merge basis for %s", message, strings.Join(unbasisable, "; "))
		}
		return "", &proposalApplyConflict{
			ProposalID: p.id,
			Paths:      conflicted,
			Strategy:   proposalApplyThreeWay,
			Changes:    proposalChangesForPaths(ctx, p, conflicted),
			Message:    message,
			Cause:      goatherd.ErrApplyConflict,
		}
	}

	ws := newLocalWorkspace(mustRepoValidator(p.repoPath))
	for _, write := range writes {
		abs, err := ws.ResolvePath(filepath.FromSlash(write.path))
		if err != nil {
			return "", fmt.Errorf("proposal %s: three_way: resolve %s: %w", p.id, write.path, err)
		}
		if write.del {
			if err := ws.Remove(ctx, abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("proposal %s: three_way: delete %s: %w", p.id, write.path, err)
			}
			continue
		}
		mode := fs.FileMode(0o644)
		if write.modeSet {
			mode = write.mode
		}
		if info, err := ws.Stat(ctx, abs); err == nil {
			mode = info.Mode().Perm()
		}
		if err := ws.WriteFile(ctx, abs, write.data, mode); err != nil {
			return "", fmt.Errorf("proposal %s: three_way: write %s: %w", p.id, write.path, err)
		}
	}

	files := p.changedFiles()
	notes := ""
	if p.mgr != nil && p.parentID != "" {
		if scope, ok := p.mgr.Get(p.parentID); ok {
			if _, err := scope.AppendFact(ctx, SchemaProposalSettled, "proposal", map[string]any{
				"proposal_id": p.id,
				"action":      "apply",
				"strategy":    proposalApplyThreeWay,
				"world":       "worktree",
				"files":       files,
			}); err != nil {
				notes = appendSettleNote(notes, "trace: settle fact: "+err.Error())
			}
		}
	}
	if err := p.recordReviewDecision(ctx, goatherd.SettleApply, nil); err != nil {
		notes = appendSettleNote(notes, "review decision: "+err.Error())
	}

	settleErr := p.output.Settle(ctx, goatherd.SettleDiscard)
	if settleErr != nil {
		// The parent already carries the merge. Do not leave the proposal
		// registered for a retry that would re-apply it; report the landed
		// effect and try to tear the stranded child down.
		if cleanupErr := teardownReleasedProposal(p); cleanupErr != nil {
			notes = appendSettleNote(notes, fmt.Sprintf("worktree teardown failed: %v; worktree stranded at %s", cleanupErr, p.workdir))
		}
		r.remove(p.id)
		message := "merge landed but sealed output could not be consumed: " + settleErr.Error()
		if errors.Is(settleErr, goatherd.ErrOutputConsumed) {
			message = fmt.Sprintf("merge landed but sealed output was already consumed by %s; inspect the parent tree", p.output.Action())
		}
		env := proposalEnvelope{
			Status:     "settled",
			ProposalID: p.id,
			Action:     "apply",
			Strategy:   proposalApplyThreeWay,
			Files:      files,
			Error:      message,
			Notes:      notes,
		}
		env.Outcome = settleOutcome(goatherd.SettleApply, p, env).String()
		return marshalProposalEnvelope(env), nil
	}
	r.remove(p.id)

	env := proposalEnvelope{
		Status:     "settled",
		ProposalID: p.id,
		Action:     "apply",
		Strategy:   proposalApplyThreeWay,
		Files:      files,
		Notes:      notes,
	}
	env.Outcome = settleOutcome(goatherd.SettleApply, p, env).String()
	return marshalProposalEnvelope(env), nil
}

// proposalPathDrifted reports whether the parent moved off the dispatch basis
// for one path, which is what decides "the parent's edit wins outright" versus
// "merge three ways".
//
// The sealed-output drift list (parentDrift) is a hint, not the authority. The
// parent git backend derives it by capturing the host tree, and that capture has
// been observed to miss a rewrite outright: a same-size edit written moments
// after the dispatch basis is intermittently absent from the drift list even
// though `git diff <basis-tree>` names it at the same instant (yaah-610z). A
// three-way apply that trusted the hint would then take the no-drift path and
// overwrite the parent's own edit whole-path, reporting "settled" with no
// conflict — a fail-open. So content decides: the path is drifted when the
// parent's current content differs from the basis, or when the path exists on
// only one side of that comparison (created or deleted in the parent). The hint
// can only add paths to the merge set, never remove them; merging a path the
// parent did not actually move is harmless (git resolves it to the proposal's
// post-image).
//
// The content comparison folds CRLF to LF because the two sides come from
// different places: base is a raw tree blob, current is the working-tree file.
// Under core.autocrlf or an eol smudge filter the same unchanged file is LF in
// the blob and CRLF on disk, so a byte-for-byte comparison would call every
// such path drifted and force a three-way merge on a path the parent never
// touched. The fold is not unconditional, though: in a repo that does not
// normalize, an EOL-only difference is a real parent edit and folding it away
// would let the no-drift fast path overwrite that edit whole-path. So the fold
// is consulted from eol (the repo's own policy) and applies only to text — see
// proposalContentEqual. The fold never reaches the merge inputs.
func proposalPathDrifted(hinted bool, path string, eol *eolNormalization, base []byte, baseExists bool, current []byte, currentExists bool) bool {
	if hinted || baseExists != currentExists {
		return true
	}
	return baseExists && !proposalContentEqual(path, eol, base, current)
}

// proposalContentEqual reports whether a basis blob and the parent's current
// file hold the same content for the drift question. Binary content is compared
// byte for byte: EOL translation does not apply to it, so a difference there is
// always a real edit. Text is compared after folding CRLF to LF, which can only
// ever drop a CR that directly precedes an LF — and only when the repo's policy
// says git normalization is what put those CRs there. Without that, an EOL-only
// difference is a parent edit.
//
// The policy is asked for last, and only once the raw bytes differ while the
// folded comparison has already come out equal: that is the single case where
// the answer can change the verdict. Byte-identical paths and paths whose edit
// is more than line endings therefore never spawn the per-path
// `git check-attr` probe (PR #824 review round 3, finding 2).
func proposalContentEqual(path string, eol *eolNormalization, base, current []byte) bool {
	if bytes.Equal(base, current) {
		return true
	}
	if isBinaryContent(base) || isBinaryContent(current) {
		return false
	}
	if !bytes.Equal(foldCRLF(base), foldCRLF(current)) {
		return false
	}
	return eol.expected(path)
}

// foldCRLF returns b with the CR of every CRLF pair removed, leaving lone CRs
// (old-Mac line endings, or content that means them) intact.
func foldCRLF(b []byte) []byte {
	if !bytes.Contains(b, []byte("\r\n")) {
		return b
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] == '\r' && i+1 < len(b) && b[i+1] == '\n' {
			continue
		}
		out = append(out, b[i])
	}
	return out
}

// proposalModeFromString parses a FileMode.String value, including the
// leading u/g/t special-bit letters. The bool distinguishes a recorded 0000
// mode from a missing mode so callers do not widen an intentionally closed
// file.
func proposalModeFromString(mode string) (fs.FileMode, bool) {
	if len(mode) < 10 {
		return 0, false
	}
	perm := mode[len(mode)-9:]
	prefix := mode[:len(mode)-9]
	bits := []fs.FileMode{0o400, 0o200, 0o100, 0o040, 0o020, 0o010, 0o004, 0o002, 0o001}
	var out fs.FileMode
	for i, bit := range bits {
		if perm[i] != '-' {
			out |= bit
		}
	}
	if strings.Contains(prefix, "u") {
		out |= fs.ModeSetuid
	}
	if strings.Contains(prefix, "g") {
		out |= fs.ModeSetgid
	}
	if strings.Contains(prefix, "t") {
		out |= fs.ModeSticky
	}
	return out, true
}

func mustRepoValidator(repoPath string) *PathValidator {
	pv := NewPathValidator(repoPath, false, nil)
	pv.WorkDir = pv.WorkspaceRoot
	return pv
}

func (p *retainedProposal) baseContentFull(ctx context.Context, rel string) ([]byte, bool, error) {
	if p.repoPath == "" {
		return nil, false, fmt.Errorf("no repository path")
	}
	state := p.basisState
	if p.output != nil {
		state = p.output.Baseline()
	}
	if state.Backend != "git" {
		return nil, false, fmt.Errorf("basis backend is %q, not git", state.Backend)
	}
	tree, _ := state.Data["tree_sha"].(string)
	if tree == "" {
		tree = state.Revision
	}
	if tree == "" {
		return nil, false, fmt.Errorf("basis has no tree revision")
	}
	size, ok, err := gitTreePathSize(ctx, p.repoPath, tree, rel)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	if size > proposalMergeMaxBytes {
		return nil, true, fmt.Errorf("file exceeds the %d-byte merge cap", proposalMergeMaxBytes)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", p.repoPath, "show", tree+":"+filepath.ToSlash(rel))
	data, err := cmd.Output()
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// gitTreePathSize distinguishes a missing path (not present in the tree) from
// an invalid/unreadable tree or object, so a broken base never becomes an empty
// merge input.
func gitTreePathSize(ctx context.Context, repoPath, tree, rel string) (int64, bool, error) {
	listed, err := exec.CommandContext(ctx, "git", "-C", repoPath, "ls-tree", "-z", tree, "--", filepath.ToSlash(rel)).Output()
	if err != nil {
		return 0, false, err
	}
	if len(bytes.TrimSpace(listed)) == 0 {
		return 0, false, nil
	}
	out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "cat-file", "-s", tree+":"+filepath.ToSlash(rel)).Output()
	if err != nil {
		return 0, false, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0, false, err
	}
	return size, true, nil
}

func (p *retainedProposal) parentContentFull(ctx context.Context, rel string) ([]byte, bool, error) {
	if p.repoPath == "" {
		return nil, false, fmt.Errorf("no repository path")
	}
	return readFullWorkspaceFile(ctx, newLocalWorkspace(mustRepoValidator(p.repoPath)), rel)
}

func (p *retainedProposal) proposedContentFull(ctx context.Context, rel string) ([]byte, bool, error) {
	if p.workdir == "" {
		return nil, false, fmt.Errorf("no worktree")
	}
	return readFullWorkspaceFile(ctx, newLocalWorkspace(mustRepoValidator(p.workdir)), rel)
}

func readFullWorkspaceFile(ctx context.Context, ws Workspace, rel string) ([]byte, bool, error) {
	abs, err := ws.ResolvePath(filepath.FromSlash(rel))
	if err != nil {
		return nil, false, err
	}
	info, err := ws.Lstat(ctx, abs)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, true, fmt.Errorf("path is not a regular file")
	}
	if info.Size() > proposalMergeMaxBytes {
		return nil, true, fmt.Errorf("file exceeds the %d-byte merge cap", proposalMergeMaxBytes)
	}
	data, err := ws.ReadFile(ctx, abs)
	if err != nil {
		return nil, true, err
	}
	return data, true, nil
}

func gitMergeFile(ctx context.Context, base, current, proposed []byte) ([]byte, bool, error) {
	dir, err := os.MkdirTemp("", "yaah-merge-")
	if err != nil {
		return nil, false, err
	}
	defer os.RemoveAll(dir)

	basePath := filepath.Join(dir, "base")
	currentPath := filepath.Join(dir, "current")
	proposedPath := filepath.Join(dir, "proposed")
	for path, data := range map[string][]byte{
		basePath:     base,
		currentPath:  current,
		proposedPath: proposed,
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, false, err
		}
	}

	cmd := exec.CommandContext(ctx, "git", "merge-file", "-p", currentPath, basePath, proposedPath)
	merged, err := cmd.Output()
	if err == nil {
		return merged, true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 && exitErr.ExitCode() <= 127 {
		return nil, false, nil
	}
	return nil, false, err
}
