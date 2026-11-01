package tools

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
	path string
	data []byte
	del  bool
	mode fs.FileMode
}

func (r *ProposalRegistry) settleWorktreeThreeWay(ctx context.Context, p *retainedProposal) (string, error) {
	if p.output == nil {
		return "", fmt.Errorf("proposal %s: three_way has no sealed worktree output", p.id)
	}
	if p.repoPath == "" {
		return "", fmt.Errorf("proposal %s: three_way has no repository path", p.id)
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
		driftSet[pathKey(normalizeProposalPath(path))] = true
	}

	var writes []proposalMergeWrite
	var conflicted []string
	for _, change := range changes {
		current, currentExists, err := p.parentContentFull(ctx, change.Path)
		if err != nil {
			return "", fmt.Errorf("proposal %s: three_way: read current %s: %w", p.id, change.Path, err)
		}
		proposed, proposedExists, err := p.proposedContentFull(ctx, change.Path)
		if err != nil {
			return "", fmt.Errorf("proposal %s: three_way: read proposed %s: %w", p.id, change.Path, err)
		}

		if !driftSet[pathKey(normalizeProposalPath(change.Path))] {
			if proposedExists {
				writes = append(writes, proposalMergeWrite{
					path: change.Path,
					data: proposed,
					mode: proposalModeFromString(change.Mode),
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
		base, baseExists, err := p.baseContentFull(ctx, change.Path)
		if err != nil {
			return "", fmt.Errorf("proposal %s: three_way: read base %s: %w", p.id, change.Path, err)
		}
		if !baseExists {
			base = nil
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
		return "", &proposalApplyConflict{
			ProposalID: p.id,
			Paths:      conflicted,
			Strategy:   proposalApplyThreeWay,
			Changes:    proposalChangesForPaths(ctx, p, conflicted),
			Message:    "three-way apply found a textual conflict; resolve the path or use discard/release",
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
		mode := write.mode
		if mode == 0 {
			mode = 0o644
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
	if settleErr != nil && !errors.Is(settleErr, goatherd.ErrOutputConsumed) {
		// The parent already carries the merge. Do not leave the proposal
		// registered for a retry that would re-apply it; report the landed
		// effect and try to tear the stranded child down.
		if cleanupErr := teardownReleasedProposal(p); cleanupErr != nil {
			notes = appendSettleNote(notes, fmt.Sprintf("worktree teardown failed: %v; worktree stranded at %s", cleanupErr, p.workdir))
		}
		r.remove(p.id)
		env := proposalEnvelope{
			Status:     "settled",
			ProposalID: p.id,
			Action:     "apply",
			Strategy:   proposalApplyThreeWay,
			Files:      files,
			Error:      "merge landed but sealed output could not be consumed: " + settleErr.Error(),
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

// proposalModeFromString parses the nine permission characters in a
// FileMode.String() value. A malformed value falls back to 0644.
func proposalModeFromString(mode string) fs.FileMode {
	if len(mode) != 10 {
		return 0o644
	}
	bits := []fs.FileMode{0o400, 0o200, 0o100, 0o040, 0o020, 0o010, 0o004, 0o002, 0o001}
	var out fs.FileMode
	for i, bit := range bits {
		if mode[i+1] != '-' {
			out |= bit
		}
	}
	return out
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
	verify := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-parse", "--verify", tree+"^{tree}")
	if _, err := verify.Output(); err != nil {
		return 0, false, err
	}
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
